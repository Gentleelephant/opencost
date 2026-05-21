package customcost

import (
	"fmt"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"go.opentelemetry.io/otel"
)

const tracerName = "github.com/opencost/opencost/pkg/customcost"

type QueryService struct {
	Querier Querier
}

func NewQueryService(querier Querier) *QueryService {
	return &QueryService{
		Querier: querier,
	}
}

// GetCustomCostTotalHandler returns aggregated custom costs.
// @Summary      查询自定义成本汇总
// @Tags         CustomCost
// @Description  返回自定义成本汇总结果，支持窗口、聚合、过滤、累计和排序。
// @Param        window         query  string  true   "查询窗口。必填。"
// @Param        aggregate      query  string  false  "聚合维度。"
// @Param        accumulate     query  string  false  "累计粒度。默认 day。"
// @Param        filter         query  string  false  "CustomCost 过滤条件。"
// @Param        costType       query  string  false  "成本类型。默认 blended。"
// @Param        sortBy         query  string  false  "排序字段。"
// @Param        sortDirection  query  string  false  "排序方向。支持 asc、desc。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      500  {string}  string
// @Router       /customCost/total [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/customCost/total [get]
func (qs *QueryService) GetCustomCostTotalHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		tracer := otel.Tracer(tracerName)
		ctx, span := tracer.Start(r.Context(), "Service.GetCustomCostTotalHandler")
		defer span.End()

		// If Query Service is nil, always return 501
		if qs == nil {
			http.Error(w, "Query Service is nil", http.StatusNotImplemented)
			return
		}

		if qs.Querier == nil {
			http.Error(w, "CustomCost Query Service is nil", http.StatusNotImplemented)
			return
		}

		qp := httputil.NewQueryParams(r.URL.Query())
		request, err := ParseCustomCostTotalRequest(qp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp, err := qs.Querier.QueryTotal(ctx, *request)
		if err != nil {
			http.Error(w, fmt.Sprintf("Internal server error: %s", err), http.StatusInternalServerError)
			return
		}

		_, spanResp := tracer.Start(ctx, "write response")
		w.Header().Set("Content-Type", "application/json")
		protocol.WriteData(w, resp)
		spanResp.End()
	}
}

// GetCustomCostTimeseriesHandler returns timeseries custom costs.
// @Summary      查询自定义成本时序
// @Tags         CustomCost
// @Description  返回自定义成本时序结果，支持窗口、聚合、过滤、累计和排序。
// @Param        window         query  string  true   "查询窗口。必填。"
// @Param        aggregate      query  string  false  "聚合维度。"
// @Param        accumulate     query  string  false  "累计粒度。默认 day。"
// @Param        filter         query  string  false  "CustomCost 过滤条件。"
// @Param        costType       query  string  false  "成本类型。默认 blended。"
// @Param        sortBy         query  string  false  "排序字段。"
// @Param        sortDirection  query  string  false  "排序方向。支持 asc、desc。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      500  {string}  string
// @Router       /customCost/timeseries [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/customCost/timeseries [get]
func (qs *QueryService) GetCustomCostTimeseriesHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		tracer := otel.Tracer(tracerName)
		ctx, span := tracer.Start(r.Context(), "Service.GetCustomCostTimeseriesHandler")
		defer span.End()

		// If Query Service is nil, always return 501
		if qs == nil {
			http.Error(w, "Query Service is nil", http.StatusNotImplemented)
			return
		}

		if qs.Querier == nil {
			http.Error(w, "CustomCost Query Service is nil", http.StatusNotImplemented)
			return
		}

		qp := httputil.NewQueryParams(r.URL.Query())
		request, err := ParseCustomCostTimeseriesRequest(qp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp, err := qs.Querier.QueryTimeseries(ctx, *request)
		if err != nil {
			http.Error(w, fmt.Sprintf("Internal server error: %s", err), http.StatusInternalServerError)
			return
		}

		_, spanResp := tracer.Start(ctx, "write response")
		w.Header().Set("Content-Type", "application/json")
		protocol.WriteData(w, resp)
		spanResp.End()
	}
}
