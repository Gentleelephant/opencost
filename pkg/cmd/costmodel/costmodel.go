package costmodel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/core/pkg/util/json"
	"github.com/opencost/opencost/pkg/cloud/models"
	"github.com/opencost/opencost/pkg/cloud/provider"
	"github.com/opencost/opencost/pkg/customcost"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"

	"github.com/opencost/opencost/core/pkg/log"
	"github.com/opencost/opencost/core/pkg/version"
	"github.com/opencost/opencost/pkg/costmodel"
	"github.com/opencost/opencost/pkg/env"
	"github.com/opencost/opencost/pkg/errors"
	"github.com/opencost/opencost/pkg/filemanager"
	"github.com/opencost/opencost/pkg/metrics"
)

// CostModelOpts contain configuration options that can be passed to the Execute() method
type CostModelOpts struct {
	// Stubbed for future configuration
}

// Healthz
// @Summary      健康检查
// @Tags         System
// @Description  服务健康探针，返回 HTTP 200 表示健康
// @Success      200  "服务健康"
// @Router       /healthz [get]
func Healthz(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	w.WriteHeader(200)
	w.Header().Set("Content-Length", "0")
	w.Header().Set("Content-Type", "text/plain")
}

func Execute(opts *CostModelOpts) error {
	log.Infof("Starting cost-model version %s", version.FriendlyVersion())
	log.Infof("Kubernetes enabled: %t", env.IsKubernetesEnabled())

	router := httprouter.New()
	var a *costmodel.Accesses
	var cp models.Provider
	if env.IsKubernetesEnabled() {
		a = costmodel.Initialize(router)
		err := StartExportWorker(context.Background(), a.Model)
		if err != nil {
			log.Errorf("couldn't start CSV export worker: %v", err)
		}

		// Register OpenCost Specific Endpoints
		router.GET(costmodel.RoutePrefix+"/allocation", a.ComputeAllocationHandler)
		router.GET(costmodel.RoutePrefix+"/allocation/summary", a.ComputeAllocationHandlerSummary)
		router.GET(costmodel.RoutePrefix+"/assets", a.ComputeAssetsHandler)
		router.GET(costmodel.RoutePrefix+"/assets/graph", a.ComputeAssetsGraphHandler)
		if env.IsCarbonEstimatesEnabled() {
			router.GET(costmodel.RoutePrefix+"/assets/carbon", a.ComputeAssetsCarbonHandler)
		}
		registerOpenCostUICompatibilityRoutes(router, a)

		// set cloud provider for cloud cost
		cp = a.CloudProvider
	}

	log.Infof("Cloud Costs enabled: %t", env.IsCloudCostEnabled())
	if env.IsCloudCostEnabled() {
		var providerConfig models.ProviderConfig
		if cp != nil {
			providerConfig = provider.ExtractConfigFromProviders(cp)
		}
		costmodel.InitializeCloudCost(router, providerConfig)
	}

	log.Infof("Custom Costs enabled: %t", env.IsCustomCostEnabled())
	var customCostPipelineService *customcost.PipelineService
	if env.IsCustomCostEnabled() {
		customCostPipelineService = costmodel.InitializeCustomCost(router)
	}

	// this endpoint is intentionally left out of the "if env.IsCustomCostEnabled()" conditional; in the handler, it is
	// valid for CustomCostPipelineService to be nil
	router.GET(costmodel.RoutePrefix+"/customCost/status", customCostPipelineService.GetCustomCostStatusHandler())

	router.GET("/healthz", Healthz)

	router.GET("/logs/level", GetLogLevel)
	router.POST("/logs/level", SetLogLevel)

	if env.IsPProfEnabled() {
		router.HandlerFunc(http.MethodGet, "/debug/pprof/", pprof.Index)
		router.HandlerFunc(http.MethodGet, "/debug/pprof/cmdline", pprof.Cmdline)
		router.HandlerFunc(http.MethodGet, "/debug/pprof/profile", pprof.Profile)
		router.HandlerFunc(http.MethodGet, "/debug/pprof/symbol", pprof.Symbol)
		router.HandlerFunc(http.MethodGet, "/debug/pprof/trace", pprof.Trace)
		router.Handler(http.MethodGet, "/debug/pprof/goroutine", pprof.Handler("goroutine"))
		router.Handler(http.MethodGet, "/debug/pprof/heap", pprof.Handler("heap"))
	}

	rootMux := http.NewServeMux()
	rootMux.Handle("/", router)
	rootMux.Handle("/metrics", promhttp.Handler())
	telemetryHandler := metrics.ResponseMetricMiddleware(rootMux)
	handler := cors.AllowAll().Handler(telemetryHandler)

	return http.ListenAndServe(fmt.Sprint(":", env.GetAPIPort()), errors.PanicHandlerMiddleware(handler))
}

func registerOpenCostUICompatibilityRoutes(router *httprouter.Router, a *costmodel.Accesses) {
	// The bundled/upstream OpenCost UI requests legacy root-relative API paths.
	// Keep the CostWize-prefixed API as canonical while accepting UI aliases.
	router.GET("/allocation", a.ComputeAllocationHandler)
	router.GET("/allocation/compute", a.ComputeAllocationHandler)
	router.GET("/allocation/summary", a.ComputeAllocationHandlerSummary)
	router.GET("/allocation/compute/summary", a.ComputeAllocationHandlerSummary)
	router.GET("/allocation/summary/topline", a.ComputeAllocationHandlerSummaryTopline)
	router.GET("/efficiency/clusters", a.ComputeAllocationHandlerClusterEfficiencySummary)
	router.GET("/assets", a.ComputeAssetsHandler)
	router.GET("/assets/graph", a.ComputeAssetsGraphHandler)
	if env.IsCarbonEstimatesEnabled() {
		router.GET("/assets/carbon", a.ComputeAssetsCarbonHandler)
	}
	router.GET("/clusterInfo", a.ClusterInfo)
	router.GET("/clusterInfoMap", a.GetClusterInfoMap)
	router.GET("/managementPlatform", a.ManagementPlatform)
	router.GET("/status", a.Status)
}

func StartExportWorker(ctx context.Context, model costmodel.AllocationModel) error {
	exportPath := env.GetExportCSVFile()
	if exportPath == "" {
		log.Infof("%s is not set, CSV export is disabled", env.ExportCSVFile)
		return nil
	}
	fm, err := filemanager.NewFileManager(exportPath)
	if err != nil {
		return fmt.Errorf("could not create file manager: %v", err)
	}
	go func() {
		log.Info("Starting CSV exporter worker...")

		// perform first update immediately
		nextRunAt := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(nextRunAt.Sub(time.Now())):
				err := costmodel.UpdateCSV(ctx, fm, model, env.GetExportCSVLabelsAll(), env.GetExportCSVLabelsList())
				if err != nil {
					// it's background worker, log error and carry on, maybe next time it will work
					log.Errorf("Error updating CSV: %s", err)
				}
				now := time.Now().UTC()
				// next launch is at 00:10 UTC tomorrow
				// extra 10 minutes is to let prometheus to collect all the data for the previous day
				nextRunAt = time.Date(now.Year(), now.Month(), now.Day(), 0, 10, 0, 0, now.Location()).AddDate(0, 0, 1)
			}
		}
	}()
	return nil
}

type LogLevelRequestResponse struct {
	Level string `json:"level"`
}

// GetLogLevel
// @Summary      获取日志级别
// @Tags         System
// @Description  获取当前服务的 zerolog 日志级别
// @Success      200  {object}  costmodel.LogLevelRequestResponse
// @Failure      500  {string}  string
// @Router       /logs/level [get]
func GetLogLevel(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	level := log.GetLogLevel()
	llrr := LogLevelRequestResponse{
		Level: level,
	}

	body, err := json.Marshal(llrr)
	if err != nil {
		http.Error(w, fmt.Sprintf("unable to retrive log level"), http.StatusInternalServerError)
		return
	}
	_, err = w.Write(body)
	if err != nil {
		http.Error(w, fmt.Sprintf("unable to write response: %s", body), http.StatusInternalServerError)
		return
	}
}

// SetLogLevel
// @Summary      设置日志级别
// @Tags         System
// @Description  设置当前服务的 zerolog 日志级别
// @Param        body  body  costmodel.LogLevelRequestResponse  true  "日志级别配置"
// @Success      200  "设置成功"
// @Failure      400  {string}  string  "无效的日志级别"
// @Router       /logs/level [post]
func SetLogLevel(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	params := LogLevelRequestResponse{}
	err := json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		http.Error(w, fmt.Sprintf("unable to decode request body, error: %s", err), http.StatusBadRequest)
		return
	}

	err = log.SetLogLevel(params.Level)
	if err != nil {
		http.Error(w, fmt.Sprintf("level must be a valid log level according to zerolog; level given: %s, error: %s", params.Level, err), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}
