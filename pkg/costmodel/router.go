package costmodel

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opencost/opencost/core/pkg/kubeconfig"
	"github.com/opencost/opencost/core/pkg/nodestats"
	"github.com/opencost/opencost/core/pkg/protocol"
	"github.com/opencost/opencost/core/pkg/source"
	"github.com/opencost/opencost/core/pkg/storage"
	"github.com/opencost/opencost/core/pkg/util/retry"
	"github.com/opencost/opencost/core/pkg/util/timeutil"
	"github.com/opencost/opencost/core/pkg/version"
	cloudconfig "github.com/opencost/opencost/pkg/cloud/config"
	"github.com/opencost/opencost/pkg/cloud/provider"
	"github.com/opencost/opencost/pkg/cloudcost"
	"github.com/opencost/opencost/pkg/config"
	"github.com/opencost/opencost/pkg/customcost"
	"github.com/opencost/opencost/pkg/metrics"
	"github.com/opencost/opencost/pkg/util/watcher"

	"github.com/julienschmidt/httprouter"

	"github.com/opencost/opencost/core/pkg/clustercache"
	"github.com/opencost/opencost/core/pkg/clusters"
	sysenv "github.com/opencost/opencost/core/pkg/env"
	"github.com/opencost/opencost/core/pkg/log"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/json"
	"github.com/opencost/opencost/modules/collector-source/pkg/collector"
	"github.com/opencost/opencost/modules/prometheus-source/pkg/prom"
	"github.com/opencost/opencost/pkg/cloud/models"
	clusterc "github.com/opencost/opencost/pkg/clustercache"
	"github.com/opencost/opencost/pkg/env"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/patrickmn/go-cache"

	"k8s.io/client-go/kubernetes"
)

const (
	RFC3339Milli         = "2006-01-02T15:04:05.000Z"
	CustomPricingSetting = "CustomPricing"
	DiscountSetting      = "Discount"
	RoutePrefix          = "/kapis/costwise.wiztelemetry.io/v1alpha1"
	defaultQueryCacheTTL = 60 * time.Second
	queryCacheTTLEnvVar  = "OPENCOST_QUERY_CACHE_TTL_SECONDS"

	// New env vars for per-class TTL overrides
	realtimeQueryCacheTTLEnvVar    = "OPENCOST_REALTIME_QUERY_CACHE_TTL_SECONDS"
	historicalQueryCacheTTLEnvVar  = "OPENCOST_HISTORICAL_QUERY_CACHE_TTL_SECONDS"
	defaultRealtimeQueryCacheTTL   = 30 * time.Second
	defaultHistoricalQueryCacheTTL = 5 * time.Minute
)

var (
	// gitCommit is set by the build system
	gitCommit string

	proto = protocol.HTTP()
)

// Accesses defines a singleton application instance, providing access to
// Prometheus, Kubernetes, the cloud provider, and caches.
type Accesses struct {
	DataSource          source.OpenCostDataSource
	KubeClientSet       kubernetes.Interface
	ClusterCache        clustercache.ClusterCache
	ClusterMap          clusters.ClusterMap
	CloudProvider       models.Provider
	ConfigFileManager   *config.ConfigFileManager
	ClusterInfoProvider clusters.ClusterInfoProvider
	Model               *CostModel
	MetricsEmitter      *CostModelMetricsEmitter
	// SettingsCache stores current state of app settings
	SettingsCache *cache.Cache
	// QueryCache stores query responses for repeated API queries
	QueryCache *cache.Cache
	// settingsSubscribers tracks channels through which changes to different
	// settings will be published in a pub/sub model
	settingsSubscribers map[string][]chan string
	settingsMutex       sync.Mutex
}

func newQueryCache() *cache.Cache {
	ttl := queryCacheBaseTTL()
	if ttl <= 0 {
		return nil
	}
	return cache.New(ttl, 2*ttl)
}

// queryCacheBaseTTL returns the global baseline TTL from OPENCOST_QUERY_CACHE_TTL_SECONDS.
func queryCacheBaseTTL() time.Duration {
	seconds := sysenv.GetInt(queryCacheTTLEnvVar, int(defaultQueryCacheTTL.Seconds()))
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// realtimeQueryCacheTTL returns the near-realtime cache TTL.
// Priority: OPENCOST_REALTIME_QUERY_CACHE_TTL_SECONDS > OPENCOST_QUERY_CACHE_TTL_SECONDS (if shorter than 30s) > 30s
func realtimeQueryCacheTTL() time.Duration {
	seconds := sysenv.GetInt(realtimeQueryCacheTTLEnvVar, -1)
	if seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	// Fall back to global config
	base := queryCacheBaseTTL()
	if base > 0 && base < defaultRealtimeQueryCacheTTL {
		return base
	}
	return defaultRealtimeQueryCacheTTL
}

// historicalQueryCacheTTL returns the historical cache TTL.
// Priority: OPENCOST_HISTORICAL_QUERY_CACHE_TTL_SECONDS > OPENCOST_QUERY_CACHE_TTL_SECONDS (if longer than 5m) > 5m
func historicalQueryCacheTTL() time.Duration {
	seconds := sysenv.GetInt(historicalQueryCacheTTLEnvVar, -1)
	if seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	// Fall back to global config
	base := queryCacheBaseTTL()
	if base > defaultHistoricalQueryCacheTTL {
		return base
	}
	return defaultHistoricalQueryCacheTTL
}

func queryCacheKey(endpoint string, r *http.Request) string {
	if r != nil && r.URL != nil {
		normalized := normalizeCacheWindow(r.URL.RawQuery)
		return fmt.Sprintf("%s:%s?%s", endpoint, canonicalCachePath(r.URL.Path), normalized)
	}
	return endpoint
}

func canonicalCachePath(path string) string {
	path = strings.TrimPrefix(path, RoutePrefix)

	switch path {
	case "/allocation/compute":
		return "/allocation"
	case "/allocation/compute/summary":
		return "/allocation/summary"
	default:
		return path
	}
}

// normalizeCacheWindow rounds relative window parameters to 5-minute buckets
// to improve cache hit rate for frontend polling. Absolute timestamp windows
// are preserved unchanged.
func normalizeCacheWindow(rawQuery string) string {
	if rawQuery == "" {
		return rawQuery
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}

	windowVal := values.Get("window")
	if windowVal == "" {
		return rawQuery
	}

	// Absolute windows contain commas (range) or timestamps with 'T'.
	// Do not normalize them.
	if strings.Contains(windowVal, ",") || strings.Contains(windowVal, "T") {
		return rawQuery
	}

	// Relative window: add a bucket key truncated to 5-minute intervals.
	bucketed := time.Now().UTC().Truncate(5 * time.Minute)
	values.Set("__normalized_at", bucketed.Format(time.RFC3339))

	return values.Encode()
}

func (a *Accesses) getQueryCacheResponse(endpoint string, r *http.Request) ([]byte, bool) {
	if a == nil || a.QueryCache == nil {
		return nil, false
	}

	val, found := a.QueryCache.Get(queryCacheKey(endpoint, r))
	if !found {
		return nil, false
	}

	resp, ok := val.([]byte)
	return resp, ok
}

func (a *Accesses) setQueryCacheResponseWithTTL(endpoint string, r *http.Request, resp []byte, ttl time.Duration) {
	if a == nil || a.QueryCache == nil || len(resp) == 0 {
		return
	}

	a.QueryCache.Set(queryCacheKey(endpoint, r), resp, ttl)
}

// cacheTTLForWindow returns an appropriate cache TTL based on the query window.
// Historical windows (ending more than 1 hour ago) use OPENCOST_HISTORICAL_QUERY_CACHE_TTL_SECONDS
// or the global baseline if longer.
// Near-realtime windows use OPENCOST_REALTIME_QUERY_CACHE_TTL_SECONDS or the global baseline if shorter.
func cacheTTLForWindow(w *opencost.Window) time.Duration {
	if w == nil || w.End() == nil {
		return cache.DefaultExpiration
	}

	windowEnd := *w.End()
	now := time.Now()

	// Historical window: ending more than 1 hour ago
	if windowEnd.Add(1 * time.Hour).Before(now) {
		return historicalQueryCacheTTL()
	}

	// Near-realtime window
	return realtimeQueryCacheTTL()
}

func filterFields(fields string, data map[string]*CostData) map[string]CostData {
	fs := strings.Split(fields, ",")
	fmap := make(map[string]bool)
	for _, f := range fs {
		fieldNameLower := strings.ToLower(f) // convert to go struct name by uppercasing first letter
		log.Debugf("to delete: %s", fieldNameLower)
		fmap[fieldNameLower] = true
	}
	filteredData := make(map[string]CostData)
	for cname, costdata := range data {
		s := reflect.TypeOf(*costdata)
		val := reflect.ValueOf(*costdata)
		costdata2 := CostData{}
		cd2 := reflect.New(reflect.Indirect(reflect.ValueOf(costdata2)).Type()).Elem()
		n := s.NumField()
		for i := 0; i < n; i++ {
			field := s.Field(i)
			value := val.Field(i)
			value2 := cd2.Field(i)
			if _, ok := fmap[strings.ToLower(field.Name)]; !ok {
				value2.Set(reflect.Value(value))
			}
		}
		filteredData[cname] = cd2.Interface().(CostData)
	}
	return filteredData
}

// ParsePercentString takes a string of expected format "N%" and returns a floating point 0.0N.
// If the "%" symbol is missing, it just returns 0.0N. Empty string is interpreted as "0%" and
// return 0.0.
func ParsePercentString(percentStr string) (float64, error) {
	if len(percentStr) == 0 {
		return 0.0, nil
	}
	if percentStr[len(percentStr)-1:] == "%" {
		percentStr = percentStr[:len(percentStr)-1]
	}
	discount, err := strconv.ParseFloat(percentStr, 64)
	if err != nil {
		return 0.0, err
	}
	discount *= 0.01

	return discount, nil
}

// adminAuthMiddleware wraps a handler and requires a Bearer token matching ADMIN_TOKEN env var when set.
// When ADMIN_TOKEN is not set, logs a deduped warning and allows the request through.
// When ADMIN_TOKEN is set, returns 401 if the Bearer token is missing or 403 if it does not match.
func adminAuthMiddleware(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		adminToken := env.GetAdminToken()
		if adminToken == "" {
			log.DedupedWarningf(5, "Admin token (ADMIN_TOKEN) not configured; write operations are unauthenticated")
			next(w, r, ps)
			return
		}
		authHeader := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			http.Error(w, "Missing or invalid authorization", http.StatusUnauthorized)
			return
		}
		bearerToken := strings.TrimPrefix(authHeader, prefix)
		if subtle.ConstantTimeCompare([]byte(bearerToken), []byte(adminToken)) != 1 {
			http.Error(w, "Missing or invalid authorization", http.StatusForbidden)
			return
		}
		next(w, r, ps)
	}
}

func WriteData(w http.ResponseWriter, data interface{}, err error) {
	if err != nil {
		proto.WriteError(w, proto.InternalServerError(err.Error()))
		return
	}

	proto.WriteData(w, data)
}

func registerGETWithPrefix(router *httprouter.Router, path string, handle httprouter.Handle) {
	router.GET(path, handle)
	router.GET(RoutePrefix+path, handle)
}

func registerPOSTWithPrefix(router *httprouter.Router, path string, handle httprouter.Handle) {
	router.POST(path, handle)
	router.POST(RoutePrefix+path, handle)
}

func registerAccessesRoutes(router *httprouter.Router, a *Accesses) {
	registerGETWithPrefix(router, "/costDataModel", a.CostDataModel)
	registerGETWithPrefix(router, "/allocation/autocomplete", a.ComputeAllocationAutocompleteHandler)
	registerGETWithPrefix(router, "/allocation/compute", a.ComputeAllocationHandler)
	registerGETWithPrefix(router, "/allocation/compute/summary", a.ComputeAllocationHandlerSummary)
	registerGETWithPrefix(router, "/efficiency/clusters", a.ComputeAllocationHandlerClusterEfficiencySummary)
	registerGETWithPrefix(router, "/efficiency/clusters/summary", a.ComputeAllocationHandlerClusterEfficiencySummary)
	registerGETWithPrefix(router, "/efficiency", a.ComputeEfficiencyHandler)
	registerGETWithPrefix(router, "/allNodePricing", a.GetAllNodePricing)
	registerGETWithPrefix(router, "/customPricing", a.GetCustomPricing)
	registerGETWithPrefix(router, "/currency", a.GetCurrency)
	registerPOSTWithPrefix(router, "/spotUpdate", a.UpdateSpotInfoConfigs)
	registerPOSTWithPrefix(router, "/athenaUpdate", a.UpdateAthenaInfoConfigs)
	registerPOSTWithPrefix(router, "/bigqueryUpdate", a.UpdateBigQueryInfoConfigs)
	registerPOSTWithPrefix(router, "/azureStorageUpdate", a.UpdateAzureStorageConfigs)
	registerPOSTWithPrefix(router, "/updateConfigByKey", a.UpdateConfigByKey)
	registerPOSTWithPrefix(router, "/refreshPricing", a.RefreshPricingData)
	registerGETWithPrefix(router, "/managementPlatform", a.ManagementPlatform)
	registerGETWithPrefix(router, "/clusterInfo", a.ClusterInfo)
	registerGETWithPrefix(router, "/clusterInfoMap", a.GetClusterInfoMap)
	registerGETWithPrefix(router, "/serviceAccountStatus", a.GetServiceAccountStatus)
	registerGETWithPrefix(router, "/pricingSourceStatus", a.GetPricingSourceStatus)
	registerGETWithPrefix(router, "/pricingSourceSummary", a.GetPricingSourceSummary)
	registerGETWithPrefix(router, "/pricingSourceCounts", a.GetPricingSourceCounts)
	registerGETWithPrefix(router, "/orphanedPods", a.GetOrphanedPods)
	registerGETWithPrefix(router, "/installNamespace", a.GetInstallNamespace)
	registerGETWithPrefix(router, "/installInfo", a.GetInstallInfo)
	registerPOSTWithPrefix(router, "/serviceKey", adminAuthMiddleware(a.AddServiceKey))
	registerGETWithPrefix(router, "/helmValues", a.GetHelmValues)
}

func registerCloudCostRoutes(router *httprouter.Router, cloudCostQueryService *cloudcost.QueryService, cloudCostPipelineService *cloudcost.PipelineService, cloudConfigController *cloudconfig.Controller) {
	registerGETWithPrefix(router, "/cloudCost", cloudCostQueryService.GetCloudCostHandler())
	registerGETWithPrefix(router, "/cloudCost/view/graph", cloudCostQueryService.GetCloudCostViewGraphHandler())
	registerGETWithPrefix(router, "/cloudCost/view/totals", cloudCostQueryService.GetCloudCostViewTotalsHandler())
	registerGETWithPrefix(router, "/cloudCost/view/table", cloudCostQueryService.GetCloudCostViewTableHandler(nil))

	registerGETWithPrefix(router, "/cloudCost/status", cloudCostPipelineService.GetCloudCostStatusHandler())
	registerGETWithPrefix(router, "/cloudCost/rebuild", adminAuthMiddleware(cloudCostPipelineService.GetCloudCostRebuildHandler()))
	registerGETWithPrefix(router, "/cloudCost/repair", adminAuthMiddleware(cloudCostPipelineService.GetCloudCostRepairHandler()))
	registerGETWithPrefix(router, "/cloud/config/export", adminAuthMiddleware(cloudConfigController.GetExportConfigHandler()))
	registerGETWithPrefix(router, "/cloud/config/enable", adminAuthMiddleware(cloudConfigController.GetEnableConfigHandler()))
	registerGETWithPrefix(router, "/cloud/config/disable", adminAuthMiddleware(cloudConfigController.GetDisableConfigHandler()))
	registerGETWithPrefix(router, "/cloud/config/delete", adminAuthMiddleware(cloudConfigController.GetDeleteConfigHandler()))
}

func registerCustomCostRoutes(router *httprouter.Router, customCostQueryService *customcost.QueryService) {
	registerGETWithPrefix(router, "/customCost/total", customCostQueryService.GetCustomCostTotalHandler())
	registerGETWithPrefix(router, "/customCost/timeseries", customCostQueryService.GetCustomCostTimeseriesHandler())
}

// RefreshPricingData needs to be called when a new node joins the fleet, since we cache the relevant subsets of pricing data to avoid storing the whole thing.
// RefreshPricingData refreshes pricing data from the configured cloud provider.
// @Summary      刷新云定价缓存
// @Tags         Pricing
// @Description  触发一次云厂商定价数据重新下载与缓存刷新。
// @Success      200  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /refreshPricing [post]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/refreshPricing [post]
func (a *Accesses) RefreshPricingData(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	err := a.CloudProvider.DownloadPricingData()
	if err != nil {
		log.Errorf("Error refreshing pricing data: %s", err.Error())
	}

	WriteData(w, nil, err)
}

// CostDataModel returns raw cost data records for a requested time window.
// @Summary      查询原始成本数据模型
// @Tags         Allocation
// @Description  返回指定时间窗口内的原始成本数据记录，可按命名空间过滤，并可只返回指定字段。
// @Param        timeWindow    query  string  true   "查询窗口时长，使用 duration 格式。示例：24h、7d"
// @Param        offset        query  string  false  "相对当前时间的偏移量，使用 duration 格式。示例：24h"
// @Param        filterFields  query  string  false  "仅返回指定字段，多个字段使用英文逗号分隔。"
// @Param        namespace     query  string  false  "按命名空间过滤。"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /costDataModel [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/costDataModel [get]
func (a *Accesses) CostDataModel(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	window := r.URL.Query().Get("timeWindow")
	offset := r.URL.Query().Get("offset")
	fields := r.URL.Query().Get("filterFields")
	namespace := r.URL.Query().Get("namespace")

	duration, err := timeutil.ParseDuration(window)
	if err != nil {
		WriteData(w, nil, fmt.Errorf("error parsing window (%s): %s", window, err))
		return
	}

	end := time.Now()
	if offset != "" {
		offsetDur, err := timeutil.ParseDuration(offset)
		if err != nil {
			WriteData(w, nil, fmt.Errorf("error parsing offset (%s): %s", offset, err))
			return
		}

		end = end.Add(-offsetDur)
	}

	start := end.Add(-duration)

	data, err := a.Model.ComputeCostData(start, end)

	// apply filter by removing if != namespace
	if namespace != "" {
		for key, costData := range data {
			if costData.Namespace != namespace {
				delete(data, key)
			}
		}
	}

	if fields != "" {
		filteredData := filterFields(fields, data)
		WriteData(w, filteredData, err)
	} else {
		WriteData(w, data, err)
	}
}

// GetAllNodePricing returns pricing data for all discovered node types.
// @Summary      查询节点定价
// @Tags         Pricing
// @Description  返回当前云提供商下所有节点规格的定价信息。
// @Success      200  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /allNodePricing [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/allNodePricing [get]
func (a *Accesses) GetAllNodePricing(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data, err := a.CloudProvider.AllNodePricing()
	WriteData(w, data, err)
}

// GetCustomPricing returns the active custom pricing configuration.
// @Summary      查询自定义定价配置
// @Tags         Pricing
// @Description  返回当前生效的自定义定价配置内容。
// @Success      200  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /customPricing [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/customPricing [get]
func (a *Accesses) GetCustomPricing(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := a.CloudProvider.GetConfig()
	WriteData(w, data, err)
}

// GetCurrency returns the configured currency code from custom pricing.
// @Summary      查询货币类型
// @Tags         Pricing
// @Description  返回当前配置的货币类型（如 $、€、¥ 等），从 customPricing 中提取。
// @Success      200  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /currency [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/currency [get]
func (a *Accesses) GetCurrency(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	config, err := a.CloudProvider.GetConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	WriteData(w, map[string]string{"currencyCode": config.CurrencyCode}, nil)
}

func (a *Accesses) updateCloudConfig(w http.ResponseWriter, r *http.Request, updateType string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := a.CloudProvider.UpdateConfig(r.Body, updateType)
	WriteData(w, data, err)
	if err == nil {
		err = a.CloudProvider.DownloadPricingData()
		if err != nil {
			log.Errorf("Error redownloading data on config update: %s", err.Error())
		}
	}
}

// UpdateSpotInfoConfigs updates spot pricing integration configuration.
// @Summary      更新 Spot 配置
// @Tags         Pricing
// @Accept       json
// @Description  使用请求体中的 JSON 更新 Spot 相关配置，并在成功后刷新定价数据。
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /spotUpdate [post]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/spotUpdate [post]
func (a *Accesses) UpdateSpotInfoConfigs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	a.updateCloudConfig(w, r, "spotinfo")
}

// UpdateAthenaInfoConfigs updates Athena billing integration configuration.
// @Summary      更新 Athena 配置
// @Tags         Pricing
// @Accept       json
// @Description  使用请求体中的 JSON 更新 Athena 账单集成配置，并在成功后刷新定价数据。
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /athenaUpdate [post]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/athenaUpdate [post]
func (a *Accesses) UpdateAthenaInfoConfigs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	a.updateCloudConfig(w, r, "athenainfo")
}

// UpdateBigQueryInfoConfigs updates BigQuery billing integration configuration.
// @Summary      更新 BigQuery 配置
// @Tags         Pricing
// @Accept       json
// @Description  使用请求体中的 JSON 更新 BigQuery 账单集成配置，并在成功后刷新定价数据。
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /bigqueryUpdate [post]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/bigqueryUpdate [post]
func (a *Accesses) UpdateBigQueryInfoConfigs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	a.updateCloudConfig(w, r, "bigqueryupdate")
}

// UpdateAzureStorageConfigs updates Azure storage billing integration configuration.
// @Summary      更新 Azure Storage 配置
// @Tags         Pricing
// @Accept       json
// @Description  使用请求体中的 JSON 更新 Azure Storage 账单集成配置，并在成功后刷新定价数据。
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /azureStorageUpdate [post]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/azureStorageUpdate [post]
func (a *Accesses) UpdateAzureStorageConfigs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	a.updateCloudConfig(w, r, "AzureStorage")
}

// UpdateConfigByKey updates cloud pricing configuration by key.
// @Summary      按 Key 更新云配置
// @Tags         Pricing
// @Accept       json
// @Description  使用请求体中的 JSON 更新指定云配置，并在成功后刷新定价数据。
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /updateConfigByKey [post]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/updateConfigByKey [post]
func (a *Accesses) UpdateConfigByKey(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	a.updateCloudConfig(w, r, "")
}

// ManagementPlatform returns the detected management platform metadata.
// @Summary      查询管理平台信息
// @Tags         Cluster
// @Description  返回当前集群所处管理平台信息，例如托管平台类型。
// @Success      200  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /managementPlatform [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/managementPlatform [get]
func (a *Accesses) ManagementPlatform(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data, err := a.CloudProvider.GetManagementPlatform()
	WriteData(w, data, err)
}

// ClusterInfo returns cluster metadata for the current cluster.
// @Summary      查询集群信息
// @Tags         Cluster
// @Description  返回当前集群的基础元数据。
// @Success      200  {object}  costmodel.Response
// @Router       /clusterInfo [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/clusterInfo [get]
func (a *Accesses) ClusterInfo(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data := a.ClusterInfoProvider.GetClusterInfo()

	WriteData(w, data, nil)
}

// GetClusterInfoMap returns the cluster map used by the cost model.
// @Summary      查询集群映射表
// @Tags         Cluster
// @Description  返回集群 ID 到集群信息的映射表。
// @Success      200  {object}  costmodel.Response
// @Router       /clusterInfoMap [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/clusterInfoMap [get]
func (a *Accesses) GetClusterInfoMap(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data := a.ClusterMap.AsMap()

	WriteData(w, data, nil)
}

// GetServiceAccountStatus returns cloud provider service account status.
// @Summary      查询服务账号状态
// @Tags         Pricing
// @Description  返回云厂商访问凭据或服务账号的状态。
// @Success      200  {object}  costmodel.Response
// @Router       /serviceAccountStatus [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/serviceAccountStatus [get]
func (a *Accesses) GetServiceAccountStatus(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	WriteData(w, a.CloudProvider.ServiceAccountStatus(), nil)
}

// GetPricingSourceStatus returns current pricing source health information.
// @Summary      查询定价源状态
// @Tags         Pricing
// @Description  返回当前启用定价源的状态信息。
// @Success      200  {object}  costmodel.Response
// @Router       /pricingSourceStatus [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/pricingSourceStatus [get]
func (a *Accesses) GetPricingSourceStatus(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data := a.CloudProvider.PricingSourceStatus()
	WriteData(w, data, nil)
}

// GetPricingSourceCounts returns counts of pricing records by source.
// @Summary      查询定价源数量统计
// @Tags         Pricing
// @Description  返回不同定价源的记录数量统计。
// @Success      200  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /pricingSourceCounts [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/pricingSourceCounts [get]
func (a *Accesses) GetPricingSourceCounts(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data, err := a.Model.GetPricingSourceCounts()
	WriteData(w, data, err)
}

// GetPricingSourceSummary returns a human-readable pricing source summary.
// @Summary      查询定价源摘要
// @Tags         Pricing
// @Description  返回当前定价源的汇总说明信息。
// @Success      200  {object}  costmodel.Response
// @Router       /pricingSourceSummary [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/pricingSourceSummary [get]
func (a *Accesses) GetPricingSourceSummary(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data := a.CloudProvider.PricingSourceSummary()
	WriteData(w, data, nil)
}

// GetOrphanedPods returns pods without owner references.
// @Summary      查询孤儿 Pod
// @Tags         Cluster
// @Description  返回当前集群中没有 OwnerReference 的 Pod 列表。
// @Success      200  {object}  array
// @Failure      500  {object}  costmodel.Response
// @Router       /orphanedPods [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/orphanedPods [get]
func (a *Accesses) GetOrphanedPods(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	podlist := a.ClusterCache.GetAllPods()

	var lonePods []*clustercache.Pod
	for _, pod := range podlist {
		if len(pod.OwnerReferences) == 0 {
			lonePods = append(lonePods, pod)
		}
	}

	body, err := json.Marshal(lonePods)
	if err != nil {
		fmt.Fprintf(w, "Error decoding pod: %s", err)
	} else {
		w.Write(body)
	}
}

// GetInstallNamespace returns the namespace where OpenCost is installed.
// @Summary      查询安装命名空间
// @Tags         Cluster
// @Description  返回当前 OpenCost 部署所在的 Kubernetes namespace。
// @Success      200  {string}  string
// @Router       /installNamespace [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/installNamespace [get]
func (a *Accesses) GetInstallNamespace(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ns := env.GetOpencostNamespace()
	w.Write([]byte(ns))
}

type InstallInfo struct {
	Containers  []ContainerInfo   `json:"containers"`
	ClusterInfo map[string]string `json:"clusterInfo"`
	Version     string            `json:"version"`
}

type ContainerInfo struct {
	ContainerName string `json:"containerName"`
	Image         string `json:"image"`
	StartTime     string `json:"startTime"`
}

// GetInstallInfo returns runtime and deployment metadata for the current OpenCost installation.
// @Summary      查询安装信息
// @Tags         Cluster
// @Description  返回 OpenCost 组件镜像、启动时间、版本以及集群规模信息。
// @Success      200  {object}  costmodel.InstallInfo
// @Failure      500  {object}  costmodel.Response
// @Router       /installInfo [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/installInfo [get]
func (a *Accesses) GetInstallInfo(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	containers, err := GetKubecostContainers(a.KubeClientSet)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to list pods: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	info := InstallInfo{
		Containers:  containers,
		ClusterInfo: make(map[string]string),
		Version:     version.FriendlyVersion(),
	}

	nodes := a.ClusterCache.GetAllNodes()
	cachePods := a.ClusterCache.GetAllPods()

	info.ClusterInfo["nodeCount"] = strconv.Itoa(len(nodes))
	info.ClusterInfo["podCount"] = strconv.Itoa(len(cachePods))

	body, err := json.Marshal(info)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error decoding pod: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

func GetKubecostContainers(kubeClientSet kubernetes.Interface) ([]ContainerInfo, error) {
	pods, err := kubeClientSet.CoreV1().Pods(env.GetOpencostNamespace()).List(context.Background(), metav1.ListOptions{
		LabelSelector: "app=cost-analyzer",
		FieldSelector: "status.phase=Running",
		Limit:         1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query kubernetes client for kubecost pods: %s", err)
	}

	// If we have zero pods either something is weird with the install since the app selector is not exposed in the helm
	// chart or more likely we are running locally - in either case Images field will return as null
	containers := make([]ContainerInfo, 0)
	if len(pods.Items) > 0 {
		for _, pod := range pods.Items {
			for _, container := range pod.Spec.Containers {
				c := ContainerInfo{
					ContainerName: container.Name,
					Image:         container.Image,
					StartTime:     pod.Status.StartTime.String(),
				}
				containers = append(containers, c)
			}
		}
	}

	return containers, nil
}

// AddServiceKey writes a GCP service account key to the configured secret path.
// @Summary      上传服务账号密钥
// @Tags         Pricing
// @Accept       application/x-www-form-urlencoded
// @Description  写入 GCP 服务账号密钥文件。该接口通常受管理员鉴权保护。
// @Param        key  formData  string  true  "GCP service account key JSON"
// @Success      200  {string}  string
// @Failure      500  {string}  string
// @Router       /serviceKey [post]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/serviceKey [post]
func (a *Accesses) AddServiceKey(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	r.ParseForm()

	key := r.PostForm.Get("key")
	k := []byte(key)
	err := os.WriteFile(env.GetGCPAuthSecretFilePath(), k, 0644)
	if err != nil {
		fmt.Fprintf(w, "Error writing service key: %s", err)
	}

	w.WriteHeader(http.StatusOK)
}

// GetHelmValues returns the Helm values used for the current installation when enabled.
// @Summary      查询 Helm Values
// @Tags         Cluster
// @Description  返回当前部署的 Helm values；如果未开启暴露则返回提示信息。
// @Success      200  {string}  string
// @Failure      500  {string}  string
// @Router       /helmValues [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/helmValues [get]
func (a *Accesses) GetHelmValues(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	encodedValues := sysenv.Get("HELM_VALUES", "")
	if encodedValues == "" {
		fmt.Fprintf(w, "Values reporting disabled")
		return
	}

	result, err := base64.StdEncoding.DecodeString(encodedValues)
	if err != nil {
		fmt.Fprintf(w, "Failed to decode encoded values: %s", err)
		return
	}

	w.Write(result)
}

func Initialize(router *httprouter.Router, additionalConfigWatchers ...*watcher.ConfigMapWatcher) *Accesses {
	var err error

	// Kubernetes API setup
	kubeClientset, err := kubeconfig.LoadKubeClient("")
	if err != nil {
		log.Fatalf("Failed to build Kubernetes client: %s", err.Error())
	}

	clusterUID, err := kubeconfig.GetClusterUID(kubeClientset)
	if err != nil {
		log.Fatalf("Failed to determine cluster UID: %s", err)
	}

	// Create Kubernetes Cluster Cache + Watchers
	k8sCache := clusterc.NewKubernetesClusterCache(kubeClientset)
	k8sCache.Run()

	// Create ConfigFileManager for synchronization of shared configuration
	confManager := config.NewConfigFileManager(nil)

	cloudProviderKey := env.GetCloudProviderAPIKey()
	cloudProvider, err := provider.NewProvider(k8sCache, cloudProviderKey, confManager)
	if err != nil {
		panic(err.Error())
	}

	// ClusterInfo Provider to provide the cluster map with local and remote cluster data
	var clusterInfoProvider clusters.ClusterInfoProvider
	if env.IsClusterInfoFileEnabled() {
		clusterInfoFile := confManager.ConfigFileAt(env.GetClusterInfoFilePath())
		clusterInfoProvider = NewConfiguredClusterInfoProvider(clusterInfoFile)
	} else {
		clusterInfoProvider = NewLocalClusterInfoProvider(kubeClientset, cloudProvider)
	}

	const maxRetries = 10
	const retryInterval = 10 * time.Second

	var fatalErr error

	ctx, cancel := context.WithCancel(context.Background())
	fn := func() (source.OpenCostDataSource, error) {
		ds, e := prom.NewDefaultPrometheusDataSource(clusterInfoProvider)
		if e != nil {
			if source.IsRetryable(e) {
				return nil, e
			}
			fatalErr = e
			cancel()
		}

		return ds, e
	}
	if env.IsCollectorDataSourceEnabled() {
		fn = func() (source.OpenCostDataSource, error) {
			store := GetDefaultCollectorStorage()
			nodeStatConf, err := NewNodeClientConfigFromEnv()
			if err != nil {
				return nil, fmt.Errorf("failed to get node client config: %w", err)
			}
			clusterConfig, err := kubeconfig.LoadKubeconfig("")
			if err != nil {
				return nil, fmt.Errorf("failed to load kube config: %w", err)
			}
			nodeStatClient := nodestats.NewNodeStatsSummaryClient(k8sCache, nodeStatConf, clusterConfig)
			ds := collector.NewDefaultCollectorDataSource(
				clusterUID,
				store,
				clusterInfoProvider,
				k8sCache,
				nodeStatClient,
			)
			return ds, nil
		}
	}

	dataSource, _ := retry.Retry(
		ctx,
		fn,
		maxRetries,
		retryInterval,
	)

	if fatalErr != nil {
		log.Fatalf("Failed to create Prometheus data source: %s", fatalErr)
		panic(fatalErr)
	}

	// Append the pricing config watcher
	installNamespace := env.GetOpencostNamespace()

	configWatchers := watcher.NewConfigMapWatchers(kubeClientset, installNamespace, additionalConfigWatchers...)
	configWatchers.AddWatcher(provider.ConfigWatcherFor(cloudProvider))
	configWatchers.AddWatcher(metrics.GetMetricsConfigWatcher())
	configWatchers.Watch()

	clusterMap := dataSource.ClusterMap()
	settingsCache := cache.New(cache.NoExpiration, cache.NoExpiration)

	costModel := NewCostModel(clusterUID, dataSource, cloudProvider, k8sCache, clusterMap, dataSource.BatchDuration())
	metricsEmitter := NewCostModelMetricsEmitter(k8sCache, cloudProvider, clusterInfoProvider, costModel)

	a := &Accesses{
		DataSource:          dataSource,
		KubeClientSet:       kubeClientset,
		ClusterCache:        k8sCache,
		ClusterMap:          clusterMap,
		CloudProvider:       cloudProvider,
		ConfigFileManager:   confManager,
		ClusterInfoProvider: clusterInfoProvider,
		Model:               costModel,
		MetricsEmitter:      metricsEmitter,
		SettingsCache:       settingsCache,
		QueryCache:          newQueryCache(),
	}

	// Initialize mechanism for subscribing to settings changes
	a.InitializeSettingsPubSub()
	err = a.CloudProvider.DownloadPricingData()
	if err != nil {
		log.Infof("Failed to download pricing data: %s", err)
	}

	if !env.IsKubecostMetricsPodEnabled() {
		a.MetricsEmitter.Start()
	}

	a.DataSource.RegisterEndPoints(RoutePrefix, router)

	registerAccessesRoutes(router, a)

	return a
}

// GetDefaultStorage retrieves the default shared storage which is required for running an opencost collector.
func GetDefaultCollectorStorage() storage.Storage {
	const warningMessage = `Failed to create local collector directory '%s' - %s.
		Did you mean to enable to collector? For persistent storage, it's recommended to use Prometheus, 
		or set a storage bucket configuration at %s. 

		%s`

	// Try bucket storage if it exists
	store, err := storage.TryGetDefaultStorage()
	if err == nil {
		return store
	}

	// Fallback to a local storage bucket
	dir := env.GetLocalCollectorDirectory()
	err = os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		log.Warnf(
			warningMessage,
			dir,
			err.Error(),
			sysenv.GetDefaultStorageConfigFilePath(),
			"Falling back to an in-memory file system for collector, which will lose any persistent storage upon restart.",
		)

		return storage.NewMemoryStorage()
	}

	return storage.NewFileStorage(dir)
}

// InitializeCloudCost Initializes Cloud Cost pipeline and querier and registers endpoints
func InitializeCloudCost(router *httprouter.Router) *cloudcost.PipelineService {
	log.Debugf("Cloud Cost config path: %s", env.GetCloudCostConfigPath())
	cloudConfigController := cloudconfig.NewMemoryController(nil)

	repo := cloudcost.NewMemoryRepository()
	cloudCostPipelineService := cloudcost.NewPipelineService(repo, cloudConfigController, cloudcost.DefaultIngestorConfiguration())
	repoQuerier := cloudcost.NewRepositoryQuerier(repo)
	cloudCostQueryService := cloudcost.NewQueryService(repoQuerier, repoQuerier)

	registerCloudCostRoutes(router, cloudCostQueryService, cloudCostPipelineService, cloudConfigController)

	return cloudCostPipelineService
}

func InitializeCustomCost(router *httprouter.Router) *customcost.PipelineService {
	hourlyRepo := customcost.NewMemoryRepository()
	dailyRepo := customcost.NewMemoryRepository()
	ingConfig := customcost.DefaultIngestorConfiguration()
	var err error
	customCostPipelineService, err := customcost.NewPipelineService(hourlyRepo, dailyRepo, ingConfig)
	if err != nil {
		log.Errorf("error instantiating custom cost pipeline service: %v", err)
		return nil
	}

	customCostQuerier := customcost.NewRepositoryQuerier(hourlyRepo, dailyRepo, ingConfig.HourlyDuration, ingConfig.DailyDuration)
	customCostQueryService := customcost.NewQueryService(customCostQuerier)

	registerCustomCostRoutes(router, customCostQueryService)

	return customCostPipelineService
}

// Response is the standard JSON envelope for API responses.
type Response struct {
	Code    int         `json:"code"`
	Status  string      `json:"status"`
	Data    interface{} `json:"data"`
	Message string      `json:"message,omitempty"`
	Warning string      `json:"warning,omitempty"`
}

// WrapData serializes data into the Response envelope and returns bytes.
func WrapData(data interface{}, err error) []byte {
	var resp []byte
	if err != nil {
		log.Errorf("Error returned to client: %s", err.Error())
		resp, _ = json.Marshal(&Response{
			Code:    http.StatusInternalServerError,
			Status:  "error",
			Message: err.Error(),
			Data:    data,
		})
	} else {
		resp, err = json.Marshal(&Response{
			Code:   http.StatusOK,
			Status: "success",
			Data:   data,
		})
		if err != nil {
			log.Errorf("error marshaling response json: %s", err.Error())
		}
	}
	return resp
}

func (a *Accesses) Status(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	proto.WriteData(w, map[string]interface{}{
		"status":  "ok",
		"version": version.FriendlyVersion(),
	})
}
