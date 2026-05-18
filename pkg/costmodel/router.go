package costmodel

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"github.com/opencost/opencost/core/pkg/util/timeutil"
	"github.com/opencost/opencost/core/pkg/version"
	"github.com/opencost/opencost/pkg/cloud/aws"
	cloudconfig "github.com/opencost/opencost/pkg/cloud/config"
	"github.com/opencost/opencost/pkg/cloud/gcp"
	"github.com/opencost/opencost/pkg/cloud/provider"
	"github.com/opencost/opencost/pkg/cloudcost"
	"github.com/opencost/opencost/pkg/config"
	clustermap "github.com/opencost/opencost/pkg/costmodel/clusters"
	"github.com/opencost/opencost/pkg/customcost"
	"github.com/opencost/opencost/pkg/kubeconfig"
	"github.com/opencost/opencost/pkg/metrics"
	"github.com/opencost/opencost/pkg/services"
	"github.com/opencost/opencost/pkg/util/watcher"

	"github.com/julienschmidt/httprouter"

	"github.com/getsentry/sentry-go"

	"github.com/opencost/opencost/core/pkg/clusters"
	sysenv "github.com/opencost/opencost/core/pkg/env"
	"github.com/opencost/opencost/core/pkg/log"
	"github.com/opencost/opencost/core/pkg/util/json"
	"github.com/opencost/opencost/pkg/cloud/azure"
	"github.com/opencost/opencost/pkg/cloud/models"
	"github.com/opencost/opencost/pkg/cloud/utils"
	"github.com/opencost/opencost/pkg/clustercache"
	"github.com/opencost/opencost/pkg/env"
	"github.com/opencost/opencost/pkg/errors"
	"github.com/opencost/opencost/pkg/prom"
	"github.com/opencost/opencost/pkg/thanos"
	prometheus "github.com/prometheus/client_golang/api"
	prometheusAPI "github.com/prometheus/client_golang/api/prometheus/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/patrickmn/go-cache"

	"k8s.io/client-go/kubernetes"
)

var sanitizePolicy = bluemonday.UGCPolicy()

const (
	RFC3339Milli         = "2006-01-02T15:04:05.000Z"
	maxCacheMinutes1d    = 11
	maxCacheMinutes2d    = 17
	maxCacheMinutes7d    = 37
	maxCacheMinutes30d   = 137
	CustomPricingSetting = "CustomPricing"
	DiscountSetting      = "Discount"
	defaultQueryCacheTTL = 60 * time.Second
	epRules              = apiPrefix + "/rules"
	queryCacheTTLEnvVar  = "OPENCOST_QUERY_CACHE_TTL_SECONDS"
	// RoutePrefix is the API path prefix for all CostWize routes
	RoutePrefix = "/kapis/costwise.wiztelemetry.io/v1alpha1"
)

var (
	// gitCommit is set by the build system
	gitCommit string
)

// Accesses defines a singleton application instance, providing access to
// Prometheus, Kubernetes, the cloud provider, and caches.
type Accesses struct {
	PrometheusClient    prometheus.Client
	ThanosClient        prometheus.Client
	KubeClientSet       kubernetes.Interface
	ClusterCache        clustercache.ClusterCache
	ClusterMap          clusters.ClusterMap
	CloudProvider       models.Provider
	ConfigFileManager   *config.ConfigFileManager
	ClusterInfoProvider clusters.ClusterInfoProvider
	Model               *CostModel
	MetricsEmitter      *CostModelMetricsEmitter
	OutOfClusterCache   *cache.Cache
	AggregateCache      *cache.Cache
	CostDataCache       *cache.Cache
	ClusterCostsCache   *cache.Cache
	QueryCache          *cache.Cache
	CacheExpiration     map[time.Duration]time.Duration
	AggAPI              Aggregator
	// SettingsCache stores current state of app settings
	SettingsCache *cache.Cache
	// settingsSubscribers tracks channels through which changes to different
	// settings will be published in a pub/sub model
	settingsSubscribers map[string][]chan string
	settingsMutex       sync.Mutex
	// registered http service instances
	httpServices services.HTTPServices
}

func newQueryCache() *cache.Cache {
	ttl := queryCacheTTL()
	if ttl <= 0 {
		return nil
	}
	return cache.New(ttl, 2*ttl)
}

func queryCacheTTL() time.Duration {
	seconds := sysenv.GetInt(queryCacheTTLEnvVar, int(defaultQueryCacheTTL.Seconds()))
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func queryCacheKey(endpoint string, r *http.Request) string {
	if r != nil && r.URL != nil {
		return fmt.Sprintf("%s:%s?%s", endpoint, r.URL.Path, r.URL.Query().Encode())
	}
	return endpoint
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

func (a *Accesses) setQueryCacheResponse(endpoint string, r *http.Request, resp []byte) {
	if a == nil || a.QueryCache == nil || len(resp) == 0 {
		return
	}

	a.QueryCache.Set(queryCacheKey(endpoint, r), resp, cache.DefaultExpiration)
}

// GetPrometheusClient decides whether the default Prometheus client or the Thanos client
// should be used.
func (a *Accesses) GetPrometheusClient(remote bool) prometheus.Client {
	// Use Thanos Client if it exists (enabled) and remote flag set
	var pc prometheus.Client

	if remote && a.ThanosClient != nil {
		pc = a.ThanosClient
	} else {
		pc = a.PrometheusClient
	}

	return pc
}

// GetCacheExpiration looks up and returns custom cache expiration for the given duration.
// If one does not exists, it returns the default cache expiration, which is defined by
// the particular cache.
func (a *Accesses) GetCacheExpiration(dur time.Duration) time.Duration {
	if expiration, ok := a.CacheExpiration[dur]; ok {
		return expiration
	}
	return cache.DefaultExpiration
}

// GetCacheRefresh determines how long to wait before refreshing the cache for the given duration,
// which is done 1 minute before we expect the cache to expire, or 1 minute if expiration is
// not found or is less than 2 minutes.
func (a *Accesses) GetCacheRefresh(dur time.Duration) time.Duration {
	expiry := a.GetCacheExpiration(dur).Minutes()
	if expiry <= 2.0 {
		return time.Minute
	}
	mins := time.Duration(expiry/2.0) * time.Minute
	return mins
}

// ClusterCostsFromCacheHandler
// @Summary      从缓存查询集群成本
// @Tags         Cluster
// @Description  从缓存中获取 24 小时集群成本数据
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/clusterCostsFromCache [get]
func (a *Accesses) ClusterCostsFromCacheHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	duration := 24 * time.Hour
	offset := time.Minute
	durationHrs := "24h"
	fmtOffset := "1m"
	pClient := a.GetPrometheusClient(true)

	key := fmt.Sprintf("%s:%s", durationHrs, fmtOffset)
	if data, valid := a.ClusterCostsCache.Get(key); valid {
		clusterCosts := data.(map[string]*ClusterCosts)
		w.Write(WrapDataWithMessage(clusterCosts, nil, "clusterCosts cache hit"))
	} else {
		data, err := a.ComputeClusterCosts(pClient, a.CloudProvider, duration, offset, true)
		w.Write(WrapDataWithMessage(data, err, fmt.Sprintf("clusterCosts cache miss: %s", key)))
	}
}

type Response struct {
	Code    int         `json:"code"`
	Status  string      `json:"status"`
	Data    interface{} `json:"data"`
	Message string      `json:"message,omitempty"`
	Warning string      `json:"warning,omitempty"`
}

// FilterFunc is a filter that returns true iff the given CostData should be filtered out, and the environment that was used as the filter criteria, if it was an aggregate
type FilterFunc func(*CostData) (bool, string)

// FilterCostData allows through only CostData that matches all the given filter functions
func FilterCostData(data map[string]*CostData, retains []FilterFunc, filters []FilterFunc) (map[string]*CostData, int, map[string]int) {
	result := make(map[string]*CostData)
	filteredEnvironments := make(map[string]int)
	filteredContainers := 0
DataLoop:
	for key, datum := range data {
		for _, rf := range retains {
			if ok, _ := rf(datum); ok {
				result[key] = datum
				// if any retain function passes, the data is retained and move on
				continue DataLoop
			}
		}
		for _, ff := range filters {
			if ok, environment := ff(datum); !ok {
				if environment != "" {
					filteredEnvironments[environment]++
				}
				filteredContainers++
				// if any filter function check fails, move on to the next datum
				continue DataLoop
			}
		}
		result[key] = datum
	}

	return result, filteredContainers, filteredEnvironments
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

func normalizeTimeParam(param string) (string, error) {
	if param == "" {
		return "", fmt.Errorf("invalid time param")
	}
	// convert days to hours
	if param[len(param)-1:] == "d" {
		count := param[:len(param)-1]
		val, err := strconv.ParseInt(count, 10, 64)
		if err != nil {
			return "", err
		}
		val = val * 24
		param = fmt.Sprintf("%dh", val)
	}

	return param, nil
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

func WrapDataWithMessage(data interface{}, err error, message string) []byte {
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
		resp, _ = json.Marshal(&Response{
			Code:    http.StatusOK,
			Status:  "success",
			Data:    data,
			Message: message,
		})
	}

	return resp
}

func WrapDataWithWarning(data interface{}, err error, warning string) []byte {
	var resp []byte

	if err != nil {
		log.Errorf("Error returned to client: %s", err.Error())
		resp, _ = json.Marshal(&Response{
			Code:    http.StatusInternalServerError,
			Status:  "error",
			Message: err.Error(),
			Warning: warning,
			Data:    data,
		})
	} else {
		resp, _ = json.Marshal(&Response{
			Code:    http.StatusOK,
			Status:  "success",
			Data:    data,
			Warning: warning,
		})
	}

	return resp
}

func WrapDataWithMessageAndWarning(data interface{}, err error, message, warning string) []byte {
	var resp []byte

	if err != nil {
		log.Errorf("Error returned to client: %s", err.Error())
		resp, _ = json.Marshal(&Response{
			Code:    http.StatusInternalServerError,
			Status:  "error",
			Message: err.Error(),
			Warning: warning,
			Data:    data,
		})
	} else {
		resp, _ = json.Marshal(&Response{
			Code:    http.StatusOK,
			Status:  "success",
			Data:    data,
			Message: message,
			Warning: warning,
		})
	}

	return resp
}

// wrapAsObjectItems wraps a slice of items into an object containing a single items list
// allows our k8s proxy methods to emulate a List() request to k8s API
func wrapAsObjectItems(items interface{}) map[string]interface{} {
	return map[string]interface{}{
		"items": items,
	}
}

// RefreshPricingData needs to be called when a new node joins the fleet, since we cache the relevant subsets of pricing data to avoid storing the whole thing.
// @Summary      刷新定价数据
// @Tags         Pricing
// @Description  当新节点加入集群时刷新云提供商定价数据缓存
// @Success      200  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/refreshPricing [post]
func (a *Accesses) RefreshPricingData(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	err := a.CloudProvider.DownloadPricingData()
	if err != nil {
		log.Errorf("Error refreshing pricing data: %s", err.Error())
	}

	w.Write(WrapData(nil, err))
}

// CostDataModel
// @Summary      查询成本数据模型
// @Tags         Cost Model
// @Description  查询指定时间窗口内的成本数据模型。当前实现允许省略 timeWindow，但结果依赖后端默认处理逻辑。
// @Param        timeWindow    query  string  false  "时间窗口；当前实现允许省略，但建议显式提供"
// @Param        offset        query  string  false  "偏移量"
// @Param        filterFields  query  string  false  "过滤字段列表"
// @Param        namespace     query  string  false  "命名空间过滤"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/costDataModel [get]
func (a *Accesses) CostDataModel(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	window := r.URL.Query().Get("timeWindow")
	offset := r.URL.Query().Get("offset")
	fields := r.URL.Query().Get("filterFields")
	namespace := r.URL.Query().Get("namespace")

	if offset != "" {
		offset = "offset " + offset
	}

	data, err := a.Model.ComputeCostData(a.PrometheusClient, a.CloudProvider, window, offset, namespace)

	if fields != "" {
		filteredData := filterFields(fields, data)
		w.Write(WrapData(filteredData, err))
	} else {
		w.Write(WrapData(data, err))
	}

}

// ClusterCosts
// @Summary      查询集群成本
// @Tags         Cluster
// @Description  查询指定时间窗口的集群成本汇总数据。当前实现会把缺失或非法参数编码为 HTTP 200 的错误响应体，而不是返回 HTTP 400。
// @Param        window  query  string  true   "时间窗口"
// @Param        offset  query  string  false  "偏移量"
// @Param        multi   query  bool    false  "是否使用 Thanos"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/clusterCosts [get]
func (a *Accesses) ClusterCosts(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	window := r.URL.Query().Get("window")
	offset := r.URL.Query().Get("offset")

	if window == "" {
		w.Write(WrapData(nil, fmt.Errorf("missing window argument")))
		return
	}
	windowDur, err := timeutil.ParseDuration(window)
	if err != nil {
		w.Write(WrapData(nil, fmt.Errorf("error parsing window (%s): %s", window, err)))
		return
	}

	// offset is not a required parameter
	var offsetDur time.Duration
	if offset != "" {
		offsetDur, err = timeutil.ParseDuration(offset)
		if err != nil {
			w.Write(WrapData(nil, fmt.Errorf("error parsing offset (%s): %s", offset, err)))
			return
		}
	}

	useThanos, _ := strconv.ParseBool(r.URL.Query().Get("multi"))

	if useThanos && !thanos.IsEnabled() {
		w.Write(WrapData(nil, fmt.Errorf("Multi=true while Thanos is not enabled.")))
		return
	}

	var client prometheus.Client
	if useThanos {
		client = a.ThanosClient
		offsetDur = thanos.OffsetDuration()

	} else {
		client = a.PrometheusClient
	}

	data, err := a.ComputeClusterCosts(client, a.CloudProvider, windowDur, offsetDur, true)
	w.Write(WrapData(data, err))
}

// ClusterCostsOverTime
// @Summary      查询集群成本变化趋势
// @Tags         Cluster
// @Description  按时间范围查询集群成本的变化趋势数据。start 和 end 需要使用 2006-01-02T15:04:05.000Z 格式；当前实现会把参数错误编码为 HTTP 200 的错误响应体。
// @Param        start   query  string  true   "开始时间，格式 2006-01-02T15:04:05.000Z"
// @Param        end     query  string  true   "结束时间，格式 2006-01-02T15:04:05.000Z"
// @Param        window  query  string  true   "时间窗口"
// @Param        offset  query  string  false  "偏移量"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/clusterCostsOverTime [get]
func (a *Accesses) ClusterCostsOverTime(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")
	window := r.URL.Query().Get("window")
	offset := r.URL.Query().Get("offset")

	if window == "" {
		w.Write(WrapData(nil, fmt.Errorf("missing window argument")))
		return
	}
	windowDur, err := timeutil.ParseDuration(window)
	if err != nil {
		w.Write(WrapData(nil, fmt.Errorf("error parsing window (%s): %s", window, err)))
		return
	}

	// offset is not a required parameter
	var offsetDur time.Duration
	if offset != "" {
		offsetDur, err = timeutil.ParseDuration(offset)
		if err != nil {
			w.Write(WrapData(nil, fmt.Errorf("error parsing offset (%s): %s", offset, err)))
			return
		}
	}

	data, err := ClusterCostsOverTime(a.PrometheusClient, a.CloudProvider, start, end, windowDur, offsetDur)
	w.Write(WrapData(data, err))
}

// CostDataModelRange
// @Summary      查询成本数据模型（范围）
// @Tags         Cost Model
// @Description  查询指定时间范围内的成本数据模型。start 和 end 需要使用 2006-01-02T15:04:05.000Z 格式；当前实现会把参数错误编码为 HTTP 200 的错误响应体。
// @Param        start        query  string  true   "开始时间，格式 2006-01-02T15:04:05.000Z"
// @Param        end          query  string  true   "结束时间，格式 2006-01-02T15:04:05.000Z"
// @Param        window       query  string  false  "窗口分辨率"
// @Param        filterFields query  string  false  "过滤字段"
// @Param        namespace    query  string  false  "命名空间"
// @Param        cluster      query  string  false  "集群"
// @Param        remote       query  string  false  "remote"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/costDataModelRange [get]
func (a *Accesses) CostDataModelRange(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	windowStr := r.URL.Query().Get("window")
	fields := r.URL.Query().Get("filterFields")
	namespace := r.URL.Query().Get("namespace")
	cluster := r.URL.Query().Get("cluster")
	remote := r.URL.Query().Get("remote")
	remoteEnabled := env.IsRemoteEnabled() && remote != "false"

	layout := "2006-01-02T15:04:05.000Z"
	start, err := time.Parse(layout, startStr)
	if err != nil {
		w.Write(WrapDataWithMessage(nil, fmt.Errorf("invalid start date: %s", startStr), fmt.Sprintf("invalid start date: %s", startStr)))
		return
	}
	end, err := time.Parse(layout, endStr)
	if err != nil {
		w.Write(WrapDataWithMessage(nil, fmt.Errorf("invalid end date: %s", endStr), fmt.Sprintf("invalid end date: %s", endStr)))
		return
	}

	window := opencost.NewWindow(&start, &end)
	if window.IsOpen() || !window.HasDuration() || window.IsNegative() {
		w.Write(WrapDataWithMessage(nil, fmt.Errorf("invalid date range: %s", window), fmt.Sprintf("invalid date range: %s", window)))
		return
	}

	resolution := time.Hour
	if resDur, err := time.ParseDuration(windowStr); err == nil {
		resolution = resDur
	}

	// Use Thanos Client if it exists (enabled) and remote flag set
	var pClient prometheus.Client
	if remote != "false" && a.ThanosClient != nil {
		pClient = a.ThanosClient
	} else {
		pClient = a.PrometheusClient
	}

	data, err := a.Model.ComputeCostDataRange(pClient, a.CloudProvider, window, resolution, namespace, cluster, remoteEnabled)
	if err != nil {
		w.Write(WrapData(nil, err))
	}
	if fields != "" {
		filteredData := filterFields(fields, data)
		w.Write(WrapData(filteredData, err))
	} else {
		w.Write(WrapData(data, err))
	}
}

func parseAggregations(customAggregation, aggregator, filterType string) (string, []string, string) {
	var key string
	var filter string
	var val []string
	if customAggregation != "" {
		key = customAggregation
		filter = filterType
		val = strings.Split(customAggregation, ",")
	} else {
		aggregations := strings.Split(aggregator, ",")
		for i, agg := range aggregations {
			aggregations[i] = "kubernetes_" + agg
		}
		key = strings.Join(aggregations, ",")
		filter = "kubernetes_" + filterType
		val = aggregations
	}
	return key, val, filter
}

// GetAllNodePricing
// @Summary      查询所有节点定价
// @Tags         Pricing
// @Description  获取集群中所有节点的定价数据
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/allNodePricing [get]
func (a *Accesses) GetAllNodePricing(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data, err := a.CloudProvider.AllNodePricing()
	w.Write(WrapData(data, err))
}

// GetCustomPricing
// @Summary      查询自定义定价配置
// @Tags         Pricing
// @Description  获取当前云提供商的自定义定价配置数据
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/customPricing [get]
func (a *Accesses) GetCustomPricing(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := a.CloudProvider.GetConfig()
	w.Write(WrapData(data, err))
}

func (a *Accesses) UpdateSpotInfoConfigs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := a.CloudProvider.UpdateConfig(r.Body, aws.SpotInfoUpdateType)
	if err != nil {
		w.Write(WrapData(data, err))
		return
	}
	w.Write(WrapData(data, err))
	err = a.CloudProvider.DownloadPricingData()
	if err != nil {
		log.Errorf("Error redownloading data on config update: %s", err.Error())
	}
	return
}

func (a *Accesses) UpdateAthenaInfoConfigs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := a.CloudProvider.UpdateConfig(r.Body, aws.AthenaInfoUpdateType)
	if err != nil {
		w.Write(WrapData(data, err))
		return
	}
	w.Write(WrapData(data, err))
	return
}

func (a *Accesses) UpdateBigQueryInfoConfigs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := a.CloudProvider.UpdateConfig(r.Body, gcp.BigqueryUpdateType)
	if err != nil {
		w.Write(WrapData(data, err))
		return
	}
	w.Write(WrapData(data, err))
	return
}

func (a *Accesses) UpdateAzureStorageConfigs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := a.CloudProvider.UpdateConfig(r.Body, azure.AzureStorageUpdateType)
	if err != nil {
		w.Write(WrapData(data, err))
		return
	}
	w.Write(WrapData(data, err))
	return
}

func (a *Accesses) UpdateConfigByKey(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := a.CloudProvider.UpdateConfig(r.Body, "")
	if err != nil {
		w.Write(WrapData(data, err))
		return
	}
	w.Write(WrapData(data, err))
	return
}

// ManagementPlatform
// @Summary      查询管理平台信息
// @Tags         System
// @Description  获取当前集群所属的管理平台信息
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/managementPlatform [get]
func (a *Accesses) ManagementPlatform(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data, err := a.CloudProvider.GetManagementPlatform()
	if err != nil {
		w.Write(WrapData(data, err))
		return
	}
	w.Write(WrapData(data, err))
	return
}

// ClusterInfo
// @Summary      查询集群信息
// @Tags         Cluster
// @Description  获取当前集群的基本信息和配置
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/clusterInfo [get]
func (a *Accesses) ClusterInfo(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data := a.ClusterInfoProvider.GetClusterInfo()

	w.Write(WrapData(data, nil))
}

// GetClusterInfoMap
// @Summary      查询集群信息映射
// @Tags         Cluster
// @Description  获取所有已知集群的信息映射表
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/clusterInfoMap [get]
func (a *Accesses) GetClusterInfoMap(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data := a.ClusterMap.AsMap()

	w.Write(WrapData(data, nil))
}

// GetServiceAccountStatus
// @Summary      查询服务账号状态
// @Tags         System
// @Description  获取云提供商服务账号的当前状态
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/serviceAccountStatus [get]
func (a *Accesses) GetServiceAccountStatus(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	w.Write(WrapData(a.CloudProvider.ServiceAccountStatus(), nil))
}

// GetPricingSourceStatus
// @Summary      查询定价源状态
// @Tags         Pricing
// @Description  获取云提供商定价数据源的当前状态
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/pricingSourceStatus [get]
func (a *Accesses) GetPricingSourceStatus(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	w.Write(WrapData(a.CloudProvider.PricingSourceStatus(), nil))
}

// GetPricingSourceCounts
// @Summary      查询定价源计数
// @Tags         Pricing
// @Description  获取各定价数据源的数量统计
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/pricingSourceCounts [get]
func (a *Accesses) GetPricingSourceCounts(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	w.Write(WrapData(a.Model.GetPricingSourceCounts()))
}

// GetPricingSourceSummary
// @Summary      查询定价源摘要
// @Tags         Pricing
// @Description  获取定价数据源的摘要信息
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/pricingSourceSummary [get]
func (a *Accesses) GetPricingSourceSummary(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	data := a.CloudProvider.PricingSourceSummary()
	w.Write(WrapData(data, nil))
}

// GetPrometheusMetadata
// @Summary      验证 Prometheus 连接
// @Tags         Diagnostics
// @Description  验证与 Prometheus 的连接状态并返回元数据
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/validatePrometheus [get]
func (a *Accesses) GetPrometheusMetadata(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	w.Write(WrapData(prom.Validate(a.PrometheusClient)))
}

// PrometheusQuery
// @Summary      Prometheus 即时查询代理
// @Tags         Prometheus
// @Description  代理执行 Prometheus 即时查询，返回原生查询结果。当前实现对缺失 query 参数返回 HTTP 200 错误响应体；仅非法 time 格式返回 HTTP 400。
// @Param        query  query  string  true   "PromQL 查询语句"
// @Param        time   query  string  false  "查询时间点，Unix 时间戳或 RFC3339"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response{message=string}
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/prometheusQuery [get]
func (a *Accesses) PrometheusQuery(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	qp := httputil.NewQueryParams(r.URL.Query())
	query := qp.Get("query", "")
	if query == "" {
		w.Write(WrapData(nil, fmt.Errorf("Query Parameter 'query' is unset'")))
		return
	}

	// Attempt to parse time as either a unix timestamp or as an RFC3339 value
	var timeVal time.Time
	timeStr := qp.Get("time", "")
	if len(timeStr) > 0 {
		if t, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
			timeVal = time.Unix(t, 0)
		} else if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
			timeVal = t
		}

		// If time is given, but not parse-able, return an error
		if timeVal.IsZero() {
			http.Error(w, fmt.Sprintf("time must be a unix timestamp or RFC3339 value; illegal value given: %s", timeStr), http.StatusBadRequest)
		}
	}

	ctx := prom.NewNamedContext(a.PrometheusClient, prom.FrontendContextName)
	body, err := ctx.RawQuery(query, timeVal)
	if err != nil {
		w.Write(WrapData(nil, fmt.Errorf("Error running query %s. Error: %s", query, err)))
		return
	}

	w.Write(body)
}

// PrometheusQueryRange
// @Summary      Prometheus 范围查询代理
// @Tags         Prometheus
// @Description  代理执行 Prometheus 范围查询，返回时间序列数据。start 和 end 需要使用 2006-01-02T15:04:05.000Z 格式；当前实现读取的步长参数名为 duration，并把参数错误写入 HTTP 200 的文本响应体。
// @Param        query  query  string  true   "PromQL 查询语句"
// @Param        start     query  string  true   "开始时间，格式 2006-01-02T15:04:05.000Z"
// @Param        end       query  string  true   "结束时间，格式 2006-01-02T15:04:05.000Z"
// @Param        duration  query  string  true   "步长，Go duration 格式，例如 300s"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/prometheusQueryRange [get]
func (a *Accesses) PrometheusQueryRange(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	qp := httputil.NewQueryParams(r.URL.Query())
	query := qp.Get("query", "")
	if query == "" {
		fmt.Fprintf(w, "Error parsing query from request parameters.")
		return
	}

	start, end, duration, err := toStartEndStep(qp)
	if err != nil {
		fmt.Fprintf(w, err.Error())
		return
	}

	ctx := prom.NewNamedContext(a.PrometheusClient, prom.FrontendContextName)
	body, err := ctx.RawQueryRange(query, start, end, duration)
	if err != nil {
		fmt.Fprintf(w, "Error running query %s. Error: %s", query, err)
		return
	}

	w.Write(body)
}

// ThanosQuery
// @Summary      Thanos 即时查询代理
// @Tags         Prometheus
// @Description  代理执行 Thanos 即时查询（需启用 Thanos）。若未启用 Thanos，当前实现返回 HTTP 200 错误响应体；仅非法 time 格式返回 HTTP 400。
// @Param        query  query  string  true   "PromQL 查询语句"
// @Param        time   query  string  false  "查询时间点"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response{message=string}
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/thanosQuery [get]
func (a *Accesses) ThanosQuery(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if !thanos.IsEnabled() {
		w.Write(WrapData(nil, fmt.Errorf("ThanosDisabled")))
		return
	}

	qp := httputil.NewQueryParams(r.URL.Query())
	query := qp.Get("query", "")
	if query == "" {
		w.Write(WrapData(nil, fmt.Errorf("Query Parameter 'query' is unset'")))
		return
	}

	// Attempt to parse time as either a unix timestamp or as an RFC3339 value
	var timeVal time.Time
	timeStr := qp.Get("time", "")
	if len(timeStr) > 0 {
		if t, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
			timeVal = time.Unix(t, 0)
		} else if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
			timeVal = t
		}

		// If time is given, but not parse-able, return an error
		if timeVal.IsZero() {
			http.Error(w, fmt.Sprintf("time must be a unix timestamp or RFC3339 value; illegal value given: %s", timeStr), http.StatusBadRequest)
		}
	}

	ctx := prom.NewNamedContext(a.ThanosClient, prom.FrontendContextName)
	body, err := ctx.RawQuery(query, timeVal)
	if err != nil {
		w.Write(WrapData(nil, fmt.Errorf("Error running query %s. Error: %s", query, err)))
		return
	}

	w.Write(body)
}

// ThanosQueryRange
// @Summary      Thanos 范围查询代理
// @Tags         Prometheus
// @Description  代理执行 Thanos 范围查询（需启用 Thanos）。start 和 end 需要使用 2006-01-02T15:04:05.000Z 格式；当前实现读取的步长参数名为 duration。若未启用 Thanos 或参数错误，当前实现返回 HTTP 200 响应体。
// @Param        query  query  string  true   "PromQL 查询语句"
// @Param        start     query  string  true   "开始时间，格式 2006-01-02T15:04:05.000Z"
// @Param        end       query  string  true   "结束时间，格式 2006-01-02T15:04:05.000Z"
// @Param        duration  query  string  true   "步长，Go duration 格式，例如 300s"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/thanosQueryRange [get]
func (a *Accesses) ThanosQueryRange(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if !thanos.IsEnabled() {
		w.Write(WrapData(nil, fmt.Errorf("ThanosDisabled")))
		return
	}

	qp := httputil.NewQueryParams(r.URL.Query())
	query := qp.Get("query", "")
	if query == "" {
		fmt.Fprintf(w, "Error parsing query from request parameters.")
		return
	}

	start, end, duration, err := toStartEndStep(qp)
	if err != nil {
		fmt.Fprintf(w, err.Error())
		return
	}

	ctx := prom.NewNamedContext(a.ThanosClient, prom.FrontendContextName)
	body, err := ctx.RawQueryRange(query, start, end, duration)
	if err != nil {
		fmt.Fprintf(w, "Error running query %s. Error: %s", query, err)
		return
	}

	w.Write(body)
}

// helper for query range proxy requests
func toStartEndStep(qp httputil.QueryParams) (start, end time.Time, step time.Duration, err error) {
	var e error

	ss := qp.Get("start", "")
	es := qp.Get("end", "")
	ds := qp.Get("duration", "")
	layout := "2006-01-02T15:04:05.000Z"

	start, e = time.Parse(layout, ss)
	if e != nil {
		err = fmt.Errorf("Error parsing time %s. Error: %s", ss, err)
		return
	}
	end, e = time.Parse(layout, es)
	if e != nil {
		err = fmt.Errorf("Error parsing time %s. Error: %s", es, err)
		return
	}
	step, e = time.ParseDuration(ds)
	if e != nil {
		err = fmt.Errorf("Error parsing duration %s. Error: %s", ds, err)
		return
	}
	err = nil

	return
}

// GetPrometheusQueueState
// @Summary      查询 Prometheus 请求队列状态
// @Tags         Diagnostics
// @Description  获取 Prometheus 和 Thanos 请求队列的当前状态
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/diagnostics/requestQueue [get]
func (a *Accesses) GetPrometheusQueueState(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	promQueueState, err := prom.GetPrometheusQueueState(a.PrometheusClient)
	if err != nil {
		w.Write(WrapData(nil, err))
		return
	}

	result := map[string]*prom.PrometheusQueueState{
		"prometheus": promQueueState,
	}

	if thanos.IsEnabled() {
		thanosQueueState, err := prom.GetPrometheusQueueState(a.ThanosClient)
		if err != nil {
			log.Warnf("Error getting Thanos queue state: %s", err)
		} else {
			result["thanos"] = thanosQueueState
		}
	}

	w.Write(WrapData(result, nil))
}

// GetPrometheusMetrics retrieves availability of Prometheus and Thanos metrics
// @Summary      查询 Prometheus 诊断指标
// @Tags         Diagnostics
// @Description  获取 Prometheus 和 Thanos 的诊断可用性指标
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/diagnostics/prometheusMetrics [get]
func (a *Accesses) GetPrometheusMetrics(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	promMetrics := prom.GetPrometheusMetrics(a.PrometheusClient, "")

	result := map[string][]*prom.PrometheusDiagnostic{
		"prometheus": promMetrics,
	}

	if thanos.IsEnabled() {
		thanosMetrics := prom.GetPrometheusMetrics(a.ThanosClient, thanos.QueryOffset())
		result["thanos"] = thanosMetrics
	}

	w.Write(WrapData(result, nil))
}

// PrometheusRecordingRules
// @Summary      Prometheus Recording Rules
// @Tags         Prometheus
// @Description  代理获取 Prometheus 的 recording rules 配置
// @Success      200  {object}  interface{}
// @Failure      500  {string}  string
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/prometheusRecordingRules [get]
func (a *Accesses) PrometheusRecordingRules(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	u := a.PrometheusClient.URL(epRules, nil)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		fmt.Fprintf(w, "Error creating Prometheus rule request: "+err.Error())
	}

	_, body, err := a.PrometheusClient.Do(r.Context(), req)
	if err != nil {
		fmt.Fprintf(w, "Error making Prometheus rule request: "+err.Error())
	} else {
		w.Write(body)
	}
}

// PrometheusConfig
// @Summary      Prometheus 配置信息
// @Tags         Prometheus
// @Description  获取 Prometheus 服务端点地址等配置信息
// @Success      200  {object}  map[string]string
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/prometheusConfig [get]
func (a *Accesses) PrometheusConfig(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	pConfig := map[string]string{
		"address": env.GetPrometheusServerEndpoint(),
	}

	body, err := json.Marshal(pConfig)
	if err != nil {
		fmt.Fprintf(w, "Error marshalling prometheus config")
	} else {
		w.Write(body)
	}
}

// PrometheusTargets
// @Summary      Prometheus Targets
// @Tags         Prometheus
// @Description  代理获取 Prometheus 的目标端点列表
// @Success      200  {object}  interface{}
// @Failure      500  {string}  string
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/prometheusTargets [get]
func (a *Accesses) PrometheusTargets(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	u := a.PrometheusClient.URL(epTargets, nil)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		fmt.Fprintf(w, "Error creating Prometheus rule request: "+err.Error())
	}

	_, body, err := a.PrometheusClient.Do(r.Context(), req)
	if err != nil {
		fmt.Fprintf(w, "Error making Prometheus rule request: "+err.Error())
	} else {
		w.Write(body)
	}
}

// GetOrphanedPods
// @Summary      查询孤立 Pod
// @Tags         System
// @Description  获取集群中没有 OwnerReference 的孤立 Pod 列表
// @Success      200  {object}  interface{}
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
		fmt.Fprintf(w, "Error decoding pod: "+err.Error())
	} else {
		w.Write(body)
	}
}

// GetInstallNamespace
// @Summary      查询安装命名空间
// @Tags         System
// @Description  获取 OpenCost 安装的 Kubernetes 命名空间
// @Success      200  {string}  string  "命名空间名称"
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/installNamespace [get]
func (a *Accesses) GetInstallNamespace(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ns := env.GetKubecostNamespace()
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

// GetInstallInfo
// @Summary      查询安装信息
// @Tags         System
// @Description  获取 OpenCost 的安装信息，包括容器信息和版本
// @Success      200  {object}  costmodel.InstallInfo
// @Failure      500  {string}  string
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/installInfo [get]
func (a *Accesses) GetInstallInfo(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	containers, err := GetKubecostContainers(a.KubeClientSet)
	if err != nil {
		writeErrorResponse(w, 500, fmt.Sprintf("Unable to list pods: %s", err.Error()))
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
		writeErrorResponse(w, 500, fmt.Sprintf("Error decoding pod: %s", err.Error()))
		return
	}

	w.Write(body)
}

func GetKubecostContainers(kubeClientSet kubernetes.Interface) ([]ContainerInfo, error) {
	pods, err := kubeClientSet.CoreV1().Pods(env.GetKubecostNamespace()).List(context.Background(), metav1.ListOptions{
		LabelSelector: "app=cost-analyzer",
		FieldSelector: "status.phase=Running",
		Limit:         1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query kubernetes client for kubecost pods: %s", err)
	}

	// If we have zero pods either something is weird with the install since the app selector is not exposed in the helm
	// chart or more likely we are running locally - in either case Images field will return as null
	var containers []ContainerInfo
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

// AddServiceKey
// @Summary      添加服务密钥
// @Tags         Configuration
// @Description  将云提供商服务密钥写入配置文件。当前实现即使未提供 key 也会返回 HTTP 200。
// @Param        key  formData  string  false  "服务密钥；当前实现未强制校验"
// @Success      200  "成功"
// @Failure      500  {string}  string  "Error writing service key"
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/serviceKey [post]
func (a *Accesses) AddServiceKey(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	r.ParseForm()

	key := r.PostForm.Get("key")
	k := []byte(key)
	err := os.WriteFile(path.Join(env.GetConfigPathWithDefault(env.DefaultConfigMountPath), "key.json"), k, 0644)
	if err != nil {
		fmt.Fprintf(w, "Error writing service key: "+err.Error())
	}

	w.WriteHeader(http.StatusOK)
}

// GetHelmValues
// @Summary      查询 Helm 配置值
// @Tags         Configuration
// @Description  获取部署时使用的 Helm values 配置
// @Success      200  {object}  costmodel.Response
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

// Status
// @Summary      查询服务状态
// @Tags         System
// @Description  获取 OpenCost 服务状态和 Prometheus 连接信息
// @Success      200  {string}  string  "使用 Prometheus 的信息"
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/status [get]
func (a *Accesses) Status(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	promServer := env.GetPrometheusServerEndpoint()

	api := prometheusAPI.NewAPI(a.PrometheusClient)
	result, err := api.Buildinfo(r.Context())
	if err != nil {
		fmt.Fprintf(w, "Using Prometheus at "+promServer+". Error: "+err.Error())
	} else {

		fmt.Fprintf(w, "Using Prometheus at "+promServer+". Version: "+result.Version)
	}
}

// captures the panic event in sentry
func capturePanicEvent(err string, stack string) {
	msg := fmt.Sprintf("Panic: %s\nStackTrace: %s\n", err, stack)
	log.Infof(msg)
	sentry.CurrentHub().CaptureEvent(&sentry.Event{
		Level:   sentry.LevelError,
		Message: msg,
	})
	sentry.Flush(5 * time.Second)
}

// handle any panics reported by the errors package
func handlePanic(p errors.Panic) bool {
	err := p.Error

	if err != nil {
		if err, ok := err.(error); ok {
			capturePanicEvent(err.Error(), p.Stack)
		}

		if err, ok := err.(string); ok {
			capturePanicEvent(err, p.Stack)
		}
	}

	// Return true to recover iff the type is http, otherwise allow kubernetes
	// to recover.
	return p.Type == errors.PanicTypeHTTP
}

func Initialize(router *httprouter.Router, additionalConfigWatchers ...*watcher.ConfigMapWatcher) *Accesses {
	var err error
	if errorReportingEnabled {
		err = sentry.Init(sentry.ClientOptions{Release: version.FriendlyVersion()})
		if err != nil {
			log.Infof("Failed to initialize sentry for error reporting")
		} else {
			err = errors.SetPanicHandler(handlePanic)
			if err != nil {
				log.Infof("Failed to set panic handler: %s", err)
			}
		}
	}

	address := env.GetPrometheusServerEndpoint()
	if address == "" {
		log.Fatalf("No address for prometheus set in $%s. Aborting.", env.PrometheusServerEndpointEnvVar)
	}

	queryConcurrency := env.GetMaxQueryConcurrency()
	log.Infof("Prometheus/Thanos Client Max Concurrency set to %d", queryConcurrency)

	timeout := 120 * time.Second
	keepAlive := 120 * time.Second
	tlsHandshakeTimeout := 10 * time.Second
	scrapeInterval := env.GetKubecostScrapeInterval()

	var rateLimitRetryOpts *prom.RateLimitRetryOpts = nil
	if env.IsPrometheusRetryOnRateLimitResponse() {
		rateLimitRetryOpts = &prom.RateLimitRetryOpts{
			MaxRetries:       env.GetPrometheusRetryOnRateLimitMaxRetries(),
			DefaultRetryWait: env.GetPrometheusRetryOnRateLimitDefaultWait(),
		}
	}

	promCli, err := prom.NewPrometheusClient(address, &prom.PrometheusClientConfig{
		Timeout:               timeout,
		KeepAlive:             keepAlive,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		TLSInsecureSkipVerify: env.GetInsecureSkipVerify(),
		RateLimitRetryOpts:    rateLimitRetryOpts,
		Auth: &prom.ClientAuth{
			Username:    env.GetDBBasicAuthUsername(),
			Password:    env.GetDBBasicAuthUserPassword(),
			BearerToken: env.GetDBBearerToken(),
		},
		QueryConcurrency:  queryConcurrency,
		QueryLogFile:      "",
		HeaderXScopeOrgId: env.GetPrometheusHeaderXScopeOrgId(),
	})
	if err != nil {
		log.Fatalf("Failed to create prometheus client, Error: %v", err)
	}

	m, err := prom.Validate(promCli)
	if err != nil || !m.Running {
		if err != nil {
			log.Errorf("Failed to query prometheus at %s. Error: %s . Troubleshooting help available at: %s", address, err.Error(), prom.PrometheusTroubleshootingURL)
		} else if !m.Running {
			log.Errorf("Prometheus at %s is not running. Troubleshooting help available at: %s", address, prom.PrometheusTroubleshootingURL)
		}
	} else {
		log.Infof("Success: retrieved the 'up' query against prometheus at: " + address)
	}

	api := prometheusAPI.NewAPI(promCli)
	result, err := api.Buildinfo(context.Background())
	if err != nil {
		log.Infof("No valid prometheus config file at %s. Error: %s . Troubleshooting help available at: %s. Ignore if using cortex/mimir/thanos here.", address, err.Error(), prom.PrometheusTroubleshootingURL)
	} else {
		log.Infof("Retrieved a prometheus config file from: %s", address)
		prometheusVersion = result.Version
	}

	if scrapeInterval == 0 {
		scrapeInterval = time.Minute
		// Lookup scrape interval for kubecost job, update if found
		si, err := prom.ScrapeIntervalFor(promCli, env.GetKubecostJobName())
		if err == nil {
			scrapeInterval = si
		}
	}

	log.Infof("Using scrape interval of %f", scrapeInterval.Seconds())

	// Kubernetes API setup
	kubeClientset, err := kubeconfig.LoadKubeClient("")
	if err != nil {
		log.Fatalf("Failed to build Kubernetes client: %s", err.Error())
	}

	// Create ConfigFileManager for synchronization of shared configuration
	confManager := config.NewConfigFileManager(&config.ConfigFileManagerOpts{
		BucketStoreConfig: env.GetKubecostConfigBucket(),
		LocalConfigPath:   "/",
	})

	configPrefix := env.GetConfigPathWithDefault("/var/configs/")

	// Create Kubernetes Cluster Cache + Watchers
	k8sCache := clustercache.NewKubernetesClusterCache(kubeClientset)
	k8sCache.Run()

	cloudProviderKey := env.GetCloudProviderAPIKey()
	cloudProvider, err := provider.NewProvider(k8sCache, cloudProviderKey, confManager)
	if err != nil {
		panic(err.Error())
	}

	// Append the pricing config watcher
	kubecostNamespace := env.GetKubecostNamespace()

	configWatchers := watcher.NewConfigMapWatchers(kubeClientset, kubecostNamespace, additionalConfigWatchers...)
	configWatchers.AddWatcher(provider.ConfigWatcherFor(cloudProvider))
	configWatchers.AddWatcher(metrics.GetMetricsConfigWatcher())
	configWatchers.Watch()

	remoteEnabled := env.IsRemoteEnabled()
	if remoteEnabled {
		info, err := cloudProvider.ClusterInfo()
		log.Infof("Saving cluster  with id:'%s', and name:'%s' to durable storage", info["id"], info["name"])
		if err != nil {
			log.Infof("Error saving cluster id %s", err.Error())
		}
		_, _, err = utils.GetOrCreateClusterMeta(info["id"], info["name"])
		if err != nil {
			log.Infof("Unable to set cluster id '%s' for cluster '%s', %s", info["id"], info["name"], err.Error())
		}
	}

	// Thanos Client
	var thanosClient prometheus.Client
	if thanos.IsEnabled() {
		thanosAddress := thanos.QueryURL()

		if thanosAddress != "" {
			thanosCli, _ := thanos.NewThanosClient(thanosAddress, &prom.PrometheusClientConfig{
				Timeout:               timeout,
				KeepAlive:             keepAlive,
				TLSHandshakeTimeout:   tlsHandshakeTimeout,
				TLSInsecureSkipVerify: env.GetInsecureSkipVerify(),
				RateLimitRetryOpts:    rateLimitRetryOpts,
				Auth: &prom.ClientAuth{
					Username:    env.GetMultiClusterBasicAuthUsername(),
					Password:    env.GetMultiClusterBasicAuthPassword(),
					BearerToken: env.GetMultiClusterBearerToken(),
				},
				QueryConcurrency: queryConcurrency,
				QueryLogFile:     env.GetQueryLoggingFile(),
			})

			_, err = prom.Validate(thanosCli)
			if err != nil {
				log.Warnf("Failed to query Thanos at %s. Error: %s.", thanosAddress, err.Error())
				thanosClient = thanosCli
			} else {
				log.Infof("Success: retrieved the 'up' query against Thanos at: " + thanosAddress)

				thanosClient = thanosCli
			}

		} else {
			log.Infof("Error resolving environment variable: $%s", env.ThanosQueryUrlEnvVar)
		}
	}

	// ClusterInfo Provider to provide the cluster map with local and remote cluster data
	var clusterInfoProvider clusters.ClusterInfoProvider
	if env.IsClusterInfoFileEnabled() {
		clusterInfoFile := confManager.ConfigFileAt(path.Join(configPrefix, "cluster-info.json"))
		clusterInfoProvider = NewConfiguredClusterInfoProvider(clusterInfoFile)
	} else {
		clusterInfoProvider = NewLocalClusterInfoProvider(kubeClientset, cloudProvider)
	}

	// Initialize ClusterMap for maintaining ClusterInfo by ClusterID
	var clusterMap clusters.ClusterMap
	if thanosClient != nil {
		clusterMap = clustermap.NewClusterMap(thanosClient, clusterInfoProvider, 10*time.Minute)
	} else {
		clusterMap = clustermap.NewClusterMap(promCli, clusterInfoProvider, 5*time.Minute)
	}

	// cache responses from model and aggregation for a default of 10 minutes;
	// clear expired responses every 20 minutes
	aggregateCache := cache.New(time.Minute*10, time.Minute*20)
	costDataCache := cache.New(time.Minute*10, time.Minute*20)
	clusterCostsCache := cache.New(cache.NoExpiration, cache.NoExpiration)
	queryCache := newQueryCache()
	outOfClusterCache := cache.New(time.Minute*5, time.Minute*10)
	settingsCache := cache.New(cache.NoExpiration, cache.NoExpiration)

	// query durations that should be cached longer should be registered here
	// use relatively prime numbers to minimize likelihood of synchronized
	// attempts at cache warming
	day := 24 * time.Hour
	cacheExpiration := map[time.Duration]time.Duration{
		day:      maxCacheMinutes1d * time.Minute,
		2 * day:  maxCacheMinutes2d * time.Minute,
		7 * day:  maxCacheMinutes7d * time.Minute,
		30 * day: maxCacheMinutes30d * time.Minute,
	}

	var pc prometheus.Client
	if thanosClient != nil {
		pc = thanosClient
	} else {
		pc = promCli
	}
	costModel := NewCostModel(pc, cloudProvider, k8sCache, clusterMap, scrapeInterval)
	metricsEmitter := NewCostModelMetricsEmitter(promCli, k8sCache, cloudProvider, clusterInfoProvider, costModel)

	a := &Accesses{
		httpServices:        services.NewCostModelServices(),
		PrometheusClient:    promCli,
		ThanosClient:        thanosClient,
		KubeClientSet:       kubeClientset,
		ClusterCache:        k8sCache,
		ClusterMap:          clusterMap,
		CloudProvider:       cloudProvider,
		ConfigFileManager:   confManager,
		ClusterInfoProvider: clusterInfoProvider,
		Model:               costModel,
		MetricsEmitter:      metricsEmitter,
		AggregateCache:      aggregateCache,
		CostDataCache:       costDataCache,
		ClusterCostsCache:   clusterCostsCache,
		QueryCache:          queryCache,
		OutOfClusterCache:   outOfClusterCache,
		SettingsCache:       settingsCache,
		CacheExpiration:     cacheExpiration,
	}

	// Use the Accesses instance, itself, as the CostModelAggregator. This is
	// confusing and unconventional, but necessary so that we can swap it
	// out for the ETL-adapted version elsewhere.
	// TODO clean this up once ETL is open-sourced.
	a.AggAPI = a

	// Initialize mechanism for subscribing to settings changes
	a.InitializeSettingsPubSub()
	err = a.CloudProvider.DownloadPricingData()
	if err != nil {
		log.Infof("Failed to download pricing data: " + err.Error())
	}

	// Warm the aggregate cache unless explicitly set to false
	if env.IsCacheWarmingEnabled() {
		log.Infof("Init: AggregateCostModel cache warming enabled")
		a.warmAggregateCostModelCache()
	} else {
		log.Infof("Init: AggregateCostModel cache warming disabled")
	}

	if !env.IsKubecostMetricsPodEnabled() {
		a.MetricsEmitter.Start()
	}

	a.httpServices.RegisterAll(router)

	router.GET(RoutePrefix+"/costDataModel", a.CostDataModel)
	router.GET(RoutePrefix+"/costDataModelRange", a.CostDataModelRange)
	router.GET(RoutePrefix+"/aggregatedCostModel", a.AggregateCostModelHandler)
	router.GET(RoutePrefix+"/allocation/compute", a.ComputeAllocationHandler)
	router.GET(RoutePrefix+"/allocation/compute/summary", a.ComputeAllocationHandlerSummary)
	router.GET(RoutePrefix+"/allocation/summary/topline", a.ComputeAllocationHandlerSummaryTopline)
	router.GET(RoutePrefix+"/efficiency/clusters", a.ComputeAllocationHandlerClusterEfficiencySummary)
	router.GET(RoutePrefix+"/allNodePricing", a.GetAllNodePricing)
	router.GET(RoutePrefix+"/customPricing", a.GetCustomPricing)
	router.POST(RoutePrefix+"/refreshPricing", a.RefreshPricingData)
	router.GET(RoutePrefix+"/clusterCostsOverTime", a.ClusterCostsOverTime)
	router.GET(RoutePrefix+"/clusterCosts", a.ClusterCosts)
	router.GET(RoutePrefix+"/clusterCostsFromCache", a.ClusterCostsFromCacheHandler)
	router.GET(RoutePrefix+"/validatePrometheus", a.GetPrometheusMetadata)
	router.GET(RoutePrefix+"/managementPlatform", a.ManagementPlatform)
	router.GET(RoutePrefix+"/clusterInfo", a.ClusterInfo)
	router.GET(RoutePrefix+"/clusterInfoMap", a.GetClusterInfoMap)
	router.GET(RoutePrefix+"/serviceAccountStatus", a.GetServiceAccountStatus)
	router.GET(RoutePrefix+"/pricingSourceStatus", a.GetPricingSourceStatus)
	router.GET(RoutePrefix+"/pricingSourceSummary", a.GetPricingSourceSummary)
	router.GET(RoutePrefix+"/pricingSourceCounts", a.GetPricingSourceCounts)

	// endpoints migrated from server
	router.GET(RoutePrefix+"/prometheusRecordingRules", a.PrometheusRecordingRules)
	router.GET(RoutePrefix+"/prometheusConfig", a.PrometheusConfig)
	router.GET(RoutePrefix+"/prometheusTargets", a.PrometheusTargets)
	router.GET(RoutePrefix+"/orphanedPods", a.GetOrphanedPods)
	router.GET(RoutePrefix+"/installNamespace", a.GetInstallNamespace)
	router.GET(RoutePrefix+"/installInfo", a.GetInstallInfo)
	router.POST(RoutePrefix+"/serviceKey", a.AddServiceKey)
	router.GET(RoutePrefix+"/helmValues", a.GetHelmValues)
	router.GET(RoutePrefix+"/status", a.Status)

	// prom query proxies
	router.GET(RoutePrefix+"/prometheusQuery", a.PrometheusQuery)
	router.GET(RoutePrefix+"/prometheusQueryRange", a.PrometheusQueryRange)
	router.GET(RoutePrefix+"/thanosQuery", a.ThanosQuery)
	router.GET(RoutePrefix+"/thanosQueryRange", a.ThanosQueryRange)

	// diagnostics
	router.GET(RoutePrefix+"/diagnostics/requestQueue", a.GetPrometheusQueueState)
	router.GET(RoutePrefix+"/diagnostics/prometheusMetrics", a.GetPrometheusMetrics)

	return a
}

// InitializeCloudCost Initializes Cloud Cost pipeline and querier and registers endpoints
func InitializeCloudCost(router *httprouter.Router, providerConfig models.ProviderConfig) {
	log.Debugf("Cloud Cost config path: %s", env.GetCloudCostConfigPath())
	cloudConfigController := cloudconfig.NewMemoryController(providerConfig)

	repo := cloudcost.NewMemoryRepository()
	cloudCostPipelineService := cloudcost.NewPipelineService(repo, cloudConfigController, cloudcost.DefaultIngestorConfiguration())
	repoQuerier := cloudcost.NewRepositoryQuerier(repo)
	cloudCostQueryService := cloudcost.NewQueryService(repoQuerier, repoQuerier)

	router.GET(RoutePrefix+"/cloud/config/export", cloudConfigController.GetExportConfigHandler())
	router.GET(RoutePrefix+"/cloud/config/enable", cloudConfigController.GetEnableConfigHandler())
	router.GET(RoutePrefix+"/cloud/config/disable", cloudConfigController.GetDisableConfigHandler())
	router.GET(RoutePrefix+"/cloud/config/delete", cloudConfigController.GetDeleteConfigHandler())

	router.GET(RoutePrefix+"/cloudCost", cloudCostQueryService.GetCloudCostHandler())
	router.GET(RoutePrefix+"/cloudCost/view/graph", cloudCostQueryService.GetCloudCostViewGraphHandler())
	router.GET(RoutePrefix+"/cloudCost/view/totals", cloudCostQueryService.GetCloudCostViewTotalsHandler())
	router.GET(RoutePrefix+"/cloudCost/view/table", cloudCostQueryService.GetCloudCostViewTableHandler())

	router.GET(RoutePrefix+"/cloudCost/status", cloudCostPipelineService.GetCloudCostStatusHandler())
	router.GET(RoutePrefix+"/cloudCost/rebuild", cloudCostPipelineService.GetCloudCostRebuildHandler())
	router.GET(RoutePrefix+"/cloudCost/repair", cloudCostPipelineService.GetCloudCostRepairHandler())
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

	router.GET(RoutePrefix+"/customCost/total", customCostQueryService.GetCustomCostTotalHandler())
	router.GET(RoutePrefix+"/customCost/timeseries", customCostQueryService.GetCustomCostTimeseriesHandler())

	return customCostPipelineService
}

// docCloudCost is a documentation-only function for swaggo
// @Summary      查询云成本数据
// @Tags         CloudCost
// @Description  查询云提供商账单成本数据
// @Param        window      query  string  false  "时间窗口"
// @Param        aggregate   query  string  false  "聚合维度"
// @Param        filter      query  string  false  "过滤条件"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost [get]
func docCloudCost() {}

// docCloudCostViewGraph is a documentation-only function for swaggo
// @Summary      查询云成本图形视图
// @Tags         CloudCost
// @Description  获取图表展示用的云成本聚合数据
// @Param        window      query  string  false  "时间窗口"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/view/graph [get]
func docCloudCostViewGraph() {}

// docCloudCostViewTotals is a documentation-only function for swaggo
// @Summary      查询云成本总计
// @Tags         CloudCost
// @Description  获取云成本总计和分类汇总数据
// @Param        window      query  string  false  "时间窗口"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/view/totals [get]
func docCloudCostViewTotals() {}

// docCloudCostViewTable is a documentation-only function for swaggo
// @Summary      查询云成本表格数据
// @Tags         CloudCost
// @Description  获取表格展示用的详细云成本数据
// @Param        window      query  string  false  "时间窗口"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/view/table [get]
func docCloudCostViewTable() {}

// docCloudCostStatus is a documentation-only function for swaggo
// @Summary      查询云成本管道状态
// @Tags         CloudCost
// @Description  获取云成本数据处理管道的当前状态
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/status [get]
func docCloudCostStatus() {}

// docCloudCostRebuild is a documentation-only function for swaggo
// @Summary      重建云成本数据
// @Tags         CloudCost
// @Description  触发云成本数据的重新计算
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/rebuild [get]
func docCloudCostRebuild() {}

// docCloudCostRepair is a documentation-only function for swaggo
// @Summary      修复云成本数据
// @Tags         CloudCost
// @Description  修复已检测到的云成本数据一致性问题
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/repair [get]
func docCloudCostRepair() {}

// docCloudConfigExport is a documentation-only function for swaggo
// @Summary      导出云配置
// @Tags         CloudConfig
// @Description  导出当前云提供商配置
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloud/config/export [get]
func docCloudConfigExport() {}

// docCloudConfigEnable is a documentation-only function for swaggo
// @Summary      启用云配置
// @Tags         CloudConfig
// @Description  启用指定的云提供商配置
// @Param        key         query  string  true   "配置键"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloud/config/enable [get]
func docCloudConfigEnable() {}

// docCloudConfigDisable is a documentation-only function for swaggo
// @Summary      禁用云配置
// @Tags         CloudConfig
// @Description  禁用指定的云提供商配置
// @Param        key         query  string  true   "配置键"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloud/config/disable [get]
func docCloudConfigDisable() {}

// docCloudConfigDelete is a documentation-only function for swaggo
// @Summary      删除云配置
// @Tags         CloudConfig
// @Description  删除指定的云提供商配置
// @Param        key         query  string  true   "配置键"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloud/config/delete [get]
func docCloudConfigDelete() {}

// docCustomCostTotal is a documentation-only function for swaggo
// @Summary      查询自定义成本总计
// @Tags         CustomCost
// @Description  查询用户自定义成本数据的总计
// @Param        window      query  string  false  "时间窗口"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/customCost/total [get]
func docCustomCostTotal() {}

// docCustomCostTimeseries is a documentation-only function for swaggo
// @Summary      查询自定义成本时间序列
// @Tags         CustomCost
// @Description  查询用户自定义成本数据的时间序列
// @Param        window      query  string  false  "时间窗口"
// @Param        metric      query  string  false  "指标名称"
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/customCost/timeseries [get]
func docCustomCostTimeseries() {}

// docCustomCostStatus is a documentation-only function for swaggo
// @Summary      查询自定义成本管道状态
// @Tags         CustomCost
// @Description  获取自定义成本数据处理管道的当前状态
// @Success      200  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/customCost/status [get]
func docCustomCostStatus() {}

func writeErrorResponse(w http.ResponseWriter, code int, message string) {
	out := map[string]string{
		"message": message,
	}
	bytes, err := json.Marshal(out)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(500)
		fmt.Fprint(w, "unable to marshall json for error")
		log.Warnf("Failed to marshall JSON for error response: %s", err.Error())
		return
	}
	w.WriteHeader(code)
	fmt.Fprint(w, string(bytes))
}
