package cloudcost

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"go.opentelemetry.io/otel"
)

const tracerName = "github.com/opencost/ooencost/pkg/cloudcost"

const (
	csvFormat = "csv"
)

// QueryService surfaces endpoints for accessing CloudCost data in raw form or for display in views
type QueryService struct {
	Querier     Querier
	ViewQuerier ViewQuerier
}

func NewQueryService(querier Querier, viewQuerier ViewQuerier) *QueryService {
	return &QueryService{
		Querier:     querier,
		ViewQuerier: viewQuerier,
	}
}

// GetCloudCostHandler returns raw cloud cost data.
// @Summary      查询云账单原始成本
// @Tags         CloudCost
// @Description  返回非 Kubernetes 云成本原始数据，支持窗口、聚合、累积和过滤。
// @Param        window      query  string  true   "查询窗口。必填，必须为闭区间 UTC 时间窗口。"
// @Param        aggregate   query  string  false  "聚合维度，多个值使用英文逗号分隔。"
// @Param        accumulate  query  string  false  "累计粒度。"
// @Param        filter      query  string  false  "CloudCost 过滤条件。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      500  {string}  string
// @Router       /cloudCost [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost [get]
func (s *QueryService) GetCloudCostHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		tracer := otel.Tracer(tracerName)
		ctx, span := tracer.Start(r.Context(), "Service.GetCloudCostHandler")
		defer span.End()

		// If Query Service is nil, always return 501
		if s == nil {
			http.Error(w, "Query Service is nil", http.StatusNotImplemented)
			return
		}

		if s.Querier == nil {
			http.Error(w, "CloudCost Query Service is nil", http.StatusNotImplemented)
			return
		}

		qp := httputil.NewQueryParams(r.URL.Query())
		request, err := ParseCloudCostRequest(qp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp, err := s.Querier.Query(ctx, *request)
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

// GetCloudCostViewGraphHandler returns graph-oriented cloud cost view data.
// @Summary      查询云成本图表视图
// @Tags         CloudCost
// @Description  返回适用于图表展示的云成本聚合结果。
// @Param        window       query  string  true   "查询窗口。必填。"
// @Param        aggregate    query  string  false  "聚合维度。"
// @Param        accumulate   query  string  false  "累计粒度。"
// @Param        filter       query  string  false  "CloudCost 过滤条件。"
// @Param        costMetric   query  string  false  "成本指标。默认 amortizedNetCost。"
// @Param        limit        query  int     false  "返回条目上限。"
// @Param        offset       query  int     false  "结果偏移量。"
// @Param        sortBy       query  string  false  "排序字段。支持 name、cost、kubernetesPercent。"
// @Param        sortByOrder  query  string  false  "排序方向。支持 asc、desc。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      500  {string}  string
// @Router       /cloudCost/view/graph [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/view/graph [get]
func (s *QueryService) GetCloudCostViewGraphHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		tracer := otel.Tracer(tracerName)
		ctx, span := tracer.Start(r.Context(), "Service.GetCloudCostViewGraphHandler")
		defer span.End()

		// If Query Service is nil, always return 501
		if s == nil {
			http.Error(w, "Query Service is nil", http.StatusNotImplemented)
			return
		}

		if s.ViewQuerier == nil {
			http.Error(w, "CloudCost Query Service is nil", http.StatusNotImplemented)
			return
		}

		qp := httputil.NewQueryParams(r.URL.Query())
		request, err := ParseCloudCostViewRequest(qp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp, err := s.ViewQuerier.QueryViewGraph(ctx, *request)
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

// GetCloudCostViewTotalsHandler returns totals for the cloud cost view.
// @Summary      查询云成本总览视图
// @Tags         CloudCost
// @Description  返回云成本总览指标，用于卡片或顶部摘要。
// @Param        window       query  string  true   "查询窗口。必填。"
// @Param        aggregate    query  string  false  "聚合维度。"
// @Param        accumulate   query  string  false  "累计粒度。"
// @Param        filter       query  string  false  "CloudCost 过滤条件。"
// @Param        costMetric   query  string  false  "成本指标。默认 amortizedNetCost。"
// @Param        sortBy       query  string  false  "排序字段。"
// @Param        sortByOrder  query  string  false  "排序方向。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      500  {string}  string
// @Router       /cloudCost/view/totals [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/view/totals [get]
func (s *QueryService) GetCloudCostViewTotalsHandler() func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		tracer := otel.Tracer(tracerName)
		ctx, span := tracer.Start(r.Context(), "Service.GetCloudCostViewTotalsHandler")
		defer span.End()

		// If Query Service is nil, always return 501
		if s == nil {
			http.Error(w, "Query Service is nil", http.StatusNotImplemented)
			return
		}

		if s.ViewQuerier == nil {
			http.Error(w, "CloudCost Query Service is nil", http.StatusNotImplemented)
			return
		}

		qp := httputil.NewQueryParams(r.URL.Query())
		request, err := ParseCloudCostViewRequest(qp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp, err := s.ViewQuerier.QueryViewTotals(ctx, *request)
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

// GetCloudCostViewTableHandler returns tabular cloud cost data.
// @Summary      查询云成本表格视图
// @Tags         CloudCost
// @Description  返回表格视图的云成本数据，支持 JSON 与 CSV 导出。
// @Param        window       query  string  true   "查询窗口。必填。"
// @Param        aggregate    query  string  false  "聚合维度。"
// @Param        accumulate   query  string  false  "累计粒度。"
// @Param        filter       query  string  false  "CloudCost 过滤条件。"
// @Param        costMetric   query  string  false  "成本指标。默认 amortizedNetCost。"
// @Param        limit        query  int     false  "返回条目上限。"
// @Param        offset       query  int     false  "结果偏移量。"
// @Param        sortBy       query  string  false  "排序字段。支持 name、cost、kubernetesPercent。"
// @Param        sortByOrder  query  string  false  "排序方向。支持 asc、desc。"
// @Param        format       query  string  false  "返回格式；传 csv 导出 CSV。"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {string}  string
// @Failure      500  {string}  string
// @Router       /cloudCost/view/table [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/cloudCost/view/table [get]
func (s *QueryService) GetCloudCostViewTableHandler(tokenHook func(ViewTableRows) string) func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Return valid handler func
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		tracer := otel.Tracer(tracerName)
		ctx, span := tracer.Start(r.Context(), "Service.GetCloudCostViewTableHandler")
		defer span.End()

		// If Query Service is nil, always return 501
		if s == nil {
			http.Error(w, "Query Service is nil", http.StatusNotImplemented)
			return
		}

		if s.ViewQuerier == nil {
			http.Error(w, "CloudCost Query Service is nil", http.StatusNotImplemented)
			return
		}

		qp := httputil.NewQueryParams(r.URL.Query())
		request, err := ParseCloudCostViewRequest(qp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		format := qp.Get("format", "json")
		if strings.HasPrefix(format, csvFormat) {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Transfer-Encoding", "chunked")
			// Set Content-Disposition to trigger browser download
			filename := fmt.Sprintf("cloudcost-%s.csv", request.Start.Format("20060102"))
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		} else {
			// By default, send JSON
			w.Header().Set("Content-Type", "application/json")
		}

		rows, err := s.ViewQuerier.QueryViewTable(ctx, *request)
		if err != nil {
			http.Error(w, fmt.Sprintf("Internal server error: %s", err), http.StatusInternalServerError)
			return
		}

		resp := protocol.NewResponse().WithData(rows)

		if tokenHook != nil {
			resp = resp.WithMeta(map[string]any{
				"token": tokenHook(rows),
			})
		}

		_, spanResp := tracer.Start(ctx, "write response")
		defer spanResp.End()
		if format == csvFormat {
			window := opencost.NewClosedWindow(request.Start, request.End)
			// Write UTF-8 BOM for Excel compatibility
			if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
				return
			}
			writeCloudCostViewTableRowsAsCSV(w, rows, window.String())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		protocol.WriteResponse(w, resp)
	}
}
