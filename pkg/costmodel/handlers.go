package costmodel

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/core/pkg/log"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"github.com/opencost/opencost/pkg/carbon"
	"github.com/opencost/opencost/pkg/env"
)

// ComputeAssetsHandler returns the assets from the CostModel.
// @Summary      查询资产数据
// @Tags         Asset
// @Description  查询集群中的原始或聚合资产数据，包括节点、磁盘、网络、负载均衡器等。
// @Description  不传 aggregate 时返回单个 AssetSet；传 aggregate=type 时返回按时间聚合后的资产集合列表。
// @Description  参数处理顺序为：先按 filter 过滤资产；如传 aggregate，再按 step 或 accumulate 组织时间桶，最后按 type 聚合。
// @Param        window     query  string  true   "时间窗口。必填。支持相对时间和绝对时间范围。示例：window=24h、window=7d、window=today、window=week、window=2026-05-01T00:00:00Z,2026-05-08T00:00:00Z"
// @Param        cluster    query  string  false  "按集群过滤资产。支持单个或多个集群，多个集群用英文逗号分隔。该参数会与 filter 做 AND 合并。示例：cluster=prod、cluster=prod,staging"
// @Param        filter     query  string  false  "资产过滤条件，使用声明式过滤语法。支持字段：assetType、name、category、cluster、project、provider、providerID、account、service、label[<key>]。常用操作：等于 field:%22value%22，不等于 field!:%22value%22，包含 field~:%22value%22，前缀 field<~:%22prefix%22；AND 使用 +，OR 使用 |。示例：filter=assetType:%22node%22、filter=cluster:%22prod%22%2Bprovider:%22aws%22、filter=label[team]:%22platform%22。注意：URL 中 + 建议编码为 %2B"
// @Param        aggregate  query  string  false  "聚合维度。当前仅支持 type。留空时返回原始资产集合；传 type 时返回按资产类型聚合后的结果。示例：aggregate=type"
// @Param        step       query  string  false  "固定时间桶宽度。仅在传 aggregate=type 时生效。支持 Go duration 风格，如 12h、24h。用于按固定时长切分窗口；不能与 accumulate 同时使用。示例：window=7d&aggregate=type&step=12h"
// @Param        accumulate query  string  false  "时间累积方式。仅在传 aggregate=type 时生效。支持：true、all、day、week、month。含义：按自然时间粒度或整个窗口累积；不能与 step 同时使用。示例：accumulate=all 返回整个窗口的汇总；accumulate=day 返回按天累积"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /assets [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/assets [get]
func (a *Accesses) ComputeAssetsHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	qp := httputil.NewQueryParams(r.URL.Query())

	// CSV export: skip cache, compute and write CSV directly.
	if isCSVRequest(qp) {
		a.computeAssetsCSV(w, r, qp)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if resp, ok := a.getQueryCacheResponse("assets", r); ok {
		w.Write(resp)
		return
	}

	qp = httputil.NewQueryParams(r.URL.Query())

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	filterString := buildAssetFilterString(qp.Get("filter", ""), qp.Get("cluster", ""))
	aggregate := qp.Get("aggregate", "")
	stepRaw := qp.Get("step", "")
	step := qp.GetDuration("step", 0)
	accumulate := opencost.ParseAccumulate(qp.Get("accumulate", ""))

	if aggregate != "" && aggregate != string(opencost.AssetTypeProp) {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: only %q is supported", opencost.AssetTypeProp), http.StatusBadRequest)
		return
	}

	if aggregate == "" {
		switch {
		case stepRaw != "":
			http.Error(w, "'step' requires 'aggregate'", http.StatusBadRequest)
			return
		case qp.Get("accumulate", "") != "":
			http.Error(w, "'accumulate' requires 'aggregate'", http.StatusBadRequest)
			return
		}

		assetSet, err := a.computeAssetsFromCostmodel(window, filterString)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting assets: %s", err), http.StatusInternalServerError)
			return
		}

		resp := WrapData(assetSet, nil)
		a.setQueryCacheResponseWithTTL("assets", r, resp, cacheTTLForWindow(&window))
		w.Write(resp)
		return
	}

	if stepRaw != "" {
		if accumulate != opencost.AccumulateOptionNone {
			http.Error(w, "'step' cannot be combined with 'accumulate'", http.StatusBadRequest)
			return
		}
		if step <= 0 {
			http.Error(w, fmt.Sprintf("Invalid 'step' parameter: %q", stepRaw), http.StatusBadRequest)
			return
		}

		asr, err := querySteppedAssetSetRange(window, filterString, defaultAssetAggregate, step, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting stepped assets: %s", err), http.StatusInternalServerError)
			return
		}

		resp := WrapData(buildAssetAggregateResponse(asr), nil)
		a.setQueryCacheResponseWithTTL("assets", r, resp, cacheTTLForWindow(&window))
		w.Write(resp)
		return
	}

	switch accumulate {
	case opencost.AccumulateOptionNone, opencost.AccumulateOptionAll, opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth:
	default:
		http.Error(w, fmt.Sprintf("Invalid 'accumulate' parameter for /assets: %q", qp.Get("accumulate", "")), http.StatusBadRequest)
		return
	}

	asr, err := queryAggregatedAssetSetRange(window, filterString, defaultAssetAggregate, accumulate, a.Model.ComputeAssets)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error getting aggregated assets: %s", err), http.StatusInternalServerError)
		return
	}

	resp := WrapData(buildAssetAggregateResponse(asr), nil)
	a.setQueryCacheResponseWithTTL("assets", r, resp, cacheTTLForWindow(&window))
	w.Write(resp)
}

// ComputeAssetsGraphHandler returns graph-ready aggregated asset costs.
// @Summary      查询资产图表数据
// @Tags         Asset
// @Description  查询资产图表数据，返回按时间分桶后的资产成本曲线。
// @Description  参数处理顺序为：先按 filter 过滤资产，再按 step 或 accumulate 组织时间桶，再按 aggregate 聚合，最后按成本降序并应用 offset/limit。
// @Description  适合资产趋势图、TopN 图表、按集群/类型/标签观察资产成本变化。
// @Description  每个时间片除 items 外，还会返回 totalCost，表示该时间片内所有图表项的总成本，可直接用于计算单项占比。
// @Param        window     query  string  true   "时间窗口。必填。支持相对时间和绝对时间范围。示例：window=24h、window=7d、window=today、window=week、window=2026-05-01T00:00:00Z,2026-05-08T00:00:00Z"
// @Param        aggregate  query  string  false  "聚合维度。默认 type。当前仅支持单个资产维度，常用值：type、name、cluster、provider、service、category、account、project、providerID、label:<key>。示例：aggregate=cluster、aggregate=service、aggregate=label:team"
// @Param        step       query  string  false  "固定时间桶宽度。支持 Go duration 风格，如 12h、24h。用于按固定时长切分窗口；不能与 accumulate 同时使用。示例：window=7d&step=12h&aggregate=type"
// @Param        accumulate query  string  false  "时间粒度。默认 day。支持：hour、day、week、month。含义：按自然时间粒度返回图表桶；不能与 step 同时使用。示例：accumulate=hour 用于 24 小时趋势；accumulate=week 用于周维度报表"
// @Param        cluster    query  string  false  "按集群过滤资产。支持单个或多个集群，多个集群用英文逗号分隔。该参数会与 filter 做 AND 合并。示例：cluster=prod、cluster=prod,staging"
// @Param        filter     query  string  false  "资产过滤条件，使用声明式过滤语法。支持字段：assetType、name、category、cluster、project、provider、providerID、account、service、label[<key>]。常用操作：等于 field:%22value%22，不等于 field!:%22value%22，包含 field~:%22value%22，前缀 field<~:%22prefix%22；AND 使用 +，OR 使用 |。示例：filter=assetType:%22node%22、filter=provider:%22aws%22%2BassetType:%22disk%22、filter=cluster:%22prod%22%2Blabel[team]:%22platform%22。注意：URL 中 + 建议编码为 %2B，避免被解释为空格"
// @Param        offset     query  int     false  "图表项偏移量。默认 0。对每个时间片内按成本降序排列后的结果进行跳过，用于分页或查看 TopN 之后的条目。示例：offset=0 返回前 N 个；offset=10 表示跳过前 10 个"
// @Param        limit      query  int     false  "每个时间片返回的最大图表项数量。默认 25。仅影响返回展示数量，不改变底层成本计算。示例：limit=10 返回每个时间片成本最高的 10 个聚合项"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /assets/graph [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/assets/graph [get]
func (a *Accesses) ComputeAssetsGraphHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	qp := httputil.NewQueryParams(r.URL.Query())

	// CSV export: compute and write CSV directly.
	if isCSVRequest(qp) {
		a.computeAssetsGraphCSV(w, r, qp)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	qp = httputil.NewQueryParams(r.URL.Query())

	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	aggregate, err := normalizeAssetAggregate(qp.Get("aggregate", ""))
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	stepRaw := qp.Get("step", "")
	step := qp.GetDuration("step", 0)
	accumulateRaw := qp.Get("accumulate", "day")
	accumulate := opencost.ParseAccumulate(accumulateRaw)

	if stepRaw != "" {
		if qp.Get("accumulate", "") != "" {
			http.Error(w, "'step' cannot be combined with 'accumulate'", http.StatusBadRequest)
			return
		}
		if step <= 0 {
			http.Error(w, fmt.Sprintf("Invalid 'step' parameter: %q", stepRaw), http.StatusBadRequest)
			return
		}
	} else {
		switch accumulate {
		case opencost.AccumulateOptionHour, opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth:
		default:
			http.Error(w, fmt.Sprintf("Invalid 'accumulate' parameter for /assets/graph: %q", accumulateRaw), http.StatusBadRequest)
			return
		}
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

	filterString := buildAssetFilterString(qp.Get("filter", ""), qp.Get("cluster", ""))

	var asr *opencost.AssetSetRange
	if stepRaw != "" {
		asr, err = querySteppedAssetSetRange(window, filterString, aggregate, step, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting stepped asset graph data: %s", err), http.StatusInternalServerError)
			return
		}
	} else {
		asr, err = queryAggregatedAssetSetRange(window, filterString, aggregate, accumulate, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting asset graph data: %s", err), http.StatusInternalServerError)
			return
		}
	}

	w.Write(WrapData(buildAssetGraphResponse(asr, offset, limit), nil))
}

// ComputeAssetsCarbonHandler returns carbon estimates for assets.
// @Summary      查询资产碳排放数据
// @Tags         Asset
// @Description  查询资产的碳足迹估算数据，基于资产成本数据推导碳排放信息。
// @Description  该路由仅在启用 Carbon Estimates 时注册；若当前部署未启用，可能返回 404。
// @Description  参数处理顺序为：先按 window 取资产，再按 filter 过滤，然后计算碳排放结果。
// @Param        window    query  string  true   "时间窗口。必填。支持相对时间和绝对时间范围。示例：window=24h、window=7d、window=today"
// @Param        cluster   query  string  false  "按集群过滤资产。支持单个或多个集群，多个集群用英文逗号分隔。该参数会与 filter 做 AND 合并。示例：cluster=prod、cluster=prod,staging"
// @Param        filter    query  string  false  "资产过滤条件，语法与 /assets、/assets/graph 保持一致。支持字段：assetType、name、category、cluster、project、provider、providerID、account、service、label[<key>]。示例：filter=assetType:%22node%22、filter=cluster:%22prod%22%2Bprovider:%22aws%22"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /assets/carbon [get]
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

	filterString := buildAssetFilterString(qp.Get("filter", ""), qp.Get("cluster", ""))

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

// computeAssetsCSV handles the CSV export path for /assets.
// It mirrors the JSON handler's query logic but writes CSV output.
func (a *Accesses) computeAssetsCSV(w http.ResponseWriter, r *http.Request, qp httputil.QueryParams) {
	window, parseErr := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if parseErr != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", parseErr), http.StatusBadRequest)
		return
	}

	filterString := buildAssetFilterString(qp.Get("filter", ""), qp.Get("cluster", ""))
	aggregate := qp.Get("aggregate", "")
	stepRaw := qp.Get("step", "")
	step := qp.GetDuration("step", 0)
	accumulate := opencost.ParseAccumulate(qp.Get("accumulate", ""))

	if aggregate != "" && aggregate != string(opencost.AssetTypeProp) {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: only %q is supported", opencost.AssetTypeProp), http.StatusBadRequest)
		return
	}

	if aggregate == "" {
		if stepRaw != "" || qp.Get("accumulate", "") != "" {
			http.Error(w, "'step' and 'accumulate' require 'aggregate'", http.StatusBadRequest)
			return
		}

		assetSet, err := a.computeAssetsFromCostmodel(window, filterString)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting assets: %s", err), http.StatusInternalServerError)
			return
		}

		// All parameters validated and data fetched; now write response.
		filename := buildCSVFilename("asset", window, aggregate)

		var buf bytes.Buffer
		truncated, err := writeAssetDetailCSV(&buf, assetSet, 10000)
		if err != nil {
			log.Errorf("failed to write asset detail CSV: %v", err)
		}
		if truncated {
			w.Header().Set("X-CSV-Truncated", "true")
		}

		setCSVDownloadHeaders(w, filename)
		if err := writeUTF8BOM(w); err != nil {
			log.Errorf("failed to write UTF-8 BOM: %v", err)
			return
		}
		if _, werr := w.Write(buf.Bytes()); werr != nil {
			log.Errorf("failed to write CSV response: %v", werr)
		}
		return
	}

	// aggregated path
	if stepRaw != "" {
		if accumulate != opencost.AccumulateOptionNone {
			http.Error(w, "'step' cannot be combined with 'accumulate'", http.StatusBadRequest)
			return
		}
		if step <= 0 {
			http.Error(w, fmt.Sprintf("Invalid 'step' parameter: %q", stepRaw), http.StatusBadRequest)
			return
		}

		rawASR, err := computeAssetSetRange(window, step, filterString, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting stepped assets: %s", err), http.StatusInternalServerError)
			return
		}
		asr := opencost.NewAssetSetRange()
		for _, as := range rawASR.Assets {
			clone := as.Clone()
			if aggErr := clone.AggregateBy([]string{defaultAssetAggregate}, nil); aggErr != nil {
				http.Error(w, fmt.Sprintf("Error aggregating assets: %s", aggErr), http.StatusInternalServerError)
				return
			}
			asr.Append(clone)
		}

		// All parameters validated and data fetched; now write response.
		filename := buildCSVFilename("asset", window, aggregate)

		var buf bytes.Buffer
		truncated, err := writeAssetAggregateCSV(&buf, asr, rawASR, 10000)
		if err != nil {
			log.Errorf("failed to write asset aggregate CSV: %v", err)
		}
		if truncated {
			w.Header().Set("X-CSV-Truncated", "true")
		}

		setCSVDownloadHeaders(w, filename)
		if err := writeUTF8BOM(w); err != nil {
			log.Errorf("failed to write UTF-8 BOM: %v", err)
			return
		}
		if _, werr := w.Write(buf.Bytes()); werr != nil {
			log.Errorf("failed to write CSV response: %v", werr)
		}
		return
	}

	switch accumulate {
	case opencost.AccumulateOptionNone, opencost.AccumulateOptionAll, opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth:
	default:
		http.Error(w, fmt.Sprintf("Invalid 'accumulate' parameter for /assets: %q", qp.Get("accumulate", "")), http.StatusBadRequest)
		return
	}

	var rawASR *opencost.AssetSetRange
	var asr *opencost.AssetSetRange

	if accumulate == opencost.AccumulateOptionNone {
		assetSet, err := computeAssetSet(window, filterString, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting assets: %s", err), http.StatusInternalServerError)
			return
		}
		rawASR = opencost.NewAssetSetRange(assetSet)
		clone := assetSet.Clone()
		if aggErr := clone.AggregateBy([]string{defaultAssetAggregate}, nil); aggErr != nil {
			http.Error(w, fmt.Sprintf("Error aggregating assets: %s", aggErr), http.StatusInternalServerError)
			return
		}
		asr = opencost.NewAssetSetRange(clone)
	} else {
		step, stepErr := assetQueryStep(accumulate)
		if stepErr != nil {
			http.Error(w, fmt.Sprintf("Error resolving accumulation step: %s", stepErr), http.StatusInternalServerError)
			return
		}
		var err error
		rawASR, err = computeAssetSetRange(window, step, filterString, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting assets: %s", err), http.StatusInternalServerError)
			return
		}
		asr = opencost.NewAssetSetRange()
		for _, as := range rawASR.Assets {
			clone := as.Clone()
			asr.Append(clone)
		}
		var accErr error
		asr, accErr = asr.Accumulate(accumulate)
		if accErr != nil {
			http.Error(w, fmt.Sprintf("Error accumulating assets: %s", accErr), http.StatusInternalServerError)
			return
		}
		for _, as := range asr.Assets {
			if aggErr := as.AggregateBy([]string{defaultAssetAggregate}, nil); aggErr != nil {
				http.Error(w, fmt.Sprintf("Error aggregating assets: %s", aggErr), http.StatusInternalServerError)
				return
			}
		}
	}

	// All parameters validated and data fetched; now write response.
	filename := buildCSVFilename("asset", window, aggregate)

	var buf bytes.Buffer
	truncated, err := writeAssetAggregateCSV(&buf, asr, rawASR, 10000)
	if err != nil {
		log.Errorf("failed to write asset aggregate CSV: %v", err)
	}
	if truncated {
		w.Header().Set("X-CSV-Truncated", "true")
	}

	setCSVDownloadHeaders(w, filename)
	if err := writeUTF8BOM(w); err != nil {
		log.Errorf("failed to write UTF-8 BOM: %v", err)
		return
	}
	if _, werr := w.Write(buf.Bytes()); werr != nil {
		log.Errorf("failed to write CSV response: %v", werr)
	}
}

// computeAssetsGraphCSV handles the CSV export path for /assets/graph.
// Exports the graph result respecting offset/limit with a 10000-row safety cap.
func (a *Accesses) computeAssetsGraphCSV(w http.ResponseWriter, r *http.Request, qp httputil.QueryParams) {
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	aggregate, err := normalizeAssetAggregate(qp.Get("aggregate", ""))
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	stepRaw := qp.Get("step", "")
	step := qp.GetDuration("step", 0)
	accumulateRaw := qp.Get("accumulate", "day")
	accumulate := opencost.ParseAccumulate(accumulateRaw)

	if stepRaw != "" {
		if qp.Get("accumulate", "") != "" {
			http.Error(w, "'step' cannot be combined with 'accumulate'", http.StatusBadRequest)
			return
		}
		if step <= 0 {
			http.Error(w, fmt.Sprintf("Invalid 'step' parameter: %q", stepRaw), http.StatusBadRequest)
			return
		}
	} else {
		switch accumulate {
		case opencost.AccumulateOptionHour, opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth:
		default:
			http.Error(w, fmt.Sprintf("Invalid 'accumulate' parameter for /assets/graph: %q", accumulateRaw), http.StatusBadRequest)
			return
		}
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

	filterString := buildAssetFilterString(qp.Get("filter", ""), qp.Get("cluster", ""))

	var asr *opencost.AssetSetRange
	if stepRaw != "" {
		asr, err = querySteppedAssetSetRange(window, filterString, aggregate, step, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting stepped asset graph data: %s", err), http.StatusInternalServerError)
			return
		}
	} else {
		asr, err = queryAggregatedAssetSetRange(window, filterString, aggregate, accumulate, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting asset graph data: %s", err), http.StatusInternalServerError)
			return
		}
	}

	// All parameters validated and data fetched; now write response.
	filename := buildCSVFilename("asset-graph", window, aggregate)

	var buf bytes.Buffer
	truncated, err := writeAssetGraphCSV(&buf, asr, offset, limit, 10000)
	if err != nil {
		log.Errorf("failed to write asset graph CSV: %v", err)
	}
	if truncated {
		w.Header().Set("X-CSV-Truncated", "true")
	}

	setCSVDownloadHeaders(w, filename)
	if err := writeUTF8BOM(w); err != nil {
		log.Errorf("failed to write UTF-8 BOM: %v", err)
		return
	}
	if _, werr := w.Write(buf.Bytes()); werr != nil {
		log.Errorf("failed to write CSV response: %v", werr)
	}
}
