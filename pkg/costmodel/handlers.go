package costmodel

import (
	"fmt"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"github.com/opencost/opencost/pkg/carbon"
	"github.com/opencost/opencost/pkg/env"
)

// ComputeAssetsHandler returns the assets from the CostModel.
// @Summary      查询资产数据
// @Tags         Asset
// @Description  查询集群中的资产数据（节点、磁盘、负载均衡器等），返回 AssetSet 数据结构
// @Param        window    query  string  true   "时间窗口，如 today, week, 7d 或 RFC3339 范围"
// @Param        filter    query  string  false  "过滤条件"
// @Param        aggregate query  string  false  "聚合维度，目前仅支持 type"
// @Param        accumulate query string  false  "累积方式，支持 true/all/day/week/month"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/assets [get]
func (a *Accesses) ComputeAssetsHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	filterString := qp.Get("filter", "")
	aggregate := qp.Get("aggregate", "")
	accumulate := opencost.ParseAccumulate(qp.Get("accumulate", ""))

	if aggregate != "" && aggregate != string(opencost.AssetTypeProp) {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: only %q is supported", opencost.AssetTypeProp), http.StatusBadRequest)
		return
	}

	if aggregate == "" {
		if qp.Get("accumulate", "") != "" {
			http.Error(w, "'accumulate' requires 'aggregate'", http.StatusBadRequest)
			return
		}

		assetSet, err := a.computeAssetsFromCostmodel(window, filterString)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting assets: %s", err), http.StatusInternalServerError)
			return
		}

		w.Write(WrapData(assetSet, nil))
		return
	}

	switch accumulate {
	case opencost.AccumulateOptionNone, opencost.AccumulateOptionAll, opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth:
	default:
		http.Error(w, fmt.Sprintf("Invalid 'accumulate' parameter for /assets: %q", qp.Get("accumulate", "")), http.StatusBadRequest)
		return
	}

	asr, err := queryAggregatedAssetSetRange(window, filterString, accumulate, a.Model.ComputeAssets)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error getting aggregated assets: %s", err), http.StatusInternalServerError)
		return
	}

	w.Write(WrapData(buildAssetAggregateResponse(asr), nil))
}

// ComputeAssetsGraphHandler returns graph-ready aggregated asset costs.
// @Summary      查询资产图表数据
// @Tags         Asset
// @Description  查询按资产类型聚合的图表数据，目前仅支持 aggregate=type
// @Param        window     query  string  true   "时间窗口，如 today, week, 7d 或 RFC3339 范围"
// @Param        aggregate  query  string  false  "聚合维度，默认 type"
// @Param        accumulate query  string  false  "时间粒度，支持 day/week/month，默认 day"
// @Param        filter     query  string  false  "过滤条件"
// @Param        offset     query  int     false  "图表项偏移量"
// @Param        limit      query  int     false  "每个时间片返回的最大图表项数量"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/assets/graph [get]
func (a *Accesses) ComputeAssetsGraphHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	aggregate := qp.Get("aggregate", string(opencost.AssetTypeProp))
	if aggregate != string(opencost.AssetTypeProp) {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: only %q is supported", opencost.AssetTypeProp), http.StatusBadRequest)
		return
	}

	accumulateRaw := qp.Get("accumulate", "day")
	accumulate := opencost.ParseAccumulate(accumulateRaw)
	switch accumulate {
	case opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth:
	default:
		http.Error(w, fmt.Sprintf("Invalid 'accumulate' parameter for /assets/graph: %q", accumulateRaw), http.StatusBadRequest)
		return
	}

	offset := qp.GetInt("offset", 0)
	if offset < 0 {
		http.Error(w, fmt.Sprintf("Invalid 'offset' parameter: %d", offset), http.StatusBadRequest)
		return
	}

	limit := qp.GetInt("limit", defaultAssetGraphLimit)
	if limit < 0 {
		http.Error(w, fmt.Sprintf("Invalid 'limit' parameter: %d", limit), http.StatusBadRequest)
		return
	}

	asr, err := queryAggregatedAssetSetRange(window, qp.Get("filter", ""), accumulate, a.Model.ComputeAssets)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error getting asset graph data: %s", err), http.StatusInternalServerError)
		return
	}

	w.Write(WrapData(buildAssetGraphResponse(asr, offset, limit), nil))
}

// ComputeAssetsCarbonHandler returns carbon estimates for assets.
// @Summary      查询资产碳排放数据
// @Tags         Asset
// @Description  查询集群资产的碳足迹估算数据，返回碳排放量信息。该路由仅在启用 Carbon Estimates 时注册，未启用时部署实例可能返回 404。
// @Param        window    query  string  true   "时间窗口"
// @Param        filter    query  string  false  "过滤条件"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/assets/carbon [get]
func (a *Accesses) ComputeAssetsCarbonHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	filterString := qp.Get("filter", "")

	assetSet, err := a.computeAssetsFromCostmodel(window, filterString)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error getting assets: %s", err), http.StatusInternalServerError)
		return
	}

	carbonEstimates, err := carbon.RelateCarbonAssets(assetSet)

	w.Write(WrapData(carbonEstimates, nil))
}

func (a *Accesses) computeAssetsFromCostmodel(window opencost.Window, filterString string) (*opencost.AssetSet, error) {
	return computeAssetSet(window, filterString, a.Model.ComputeAssets)
}
