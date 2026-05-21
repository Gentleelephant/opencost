package costmodel

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"

	"github.com/opencost/opencost/core/pkg/filter/allocation"
	"github.com/opencost/opencost/core/pkg/log"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"github.com/opencost/opencost/core/pkg/util/timeutil"
	"github.com/opencost/opencost/pkg/env"
)

const (
	// SplitTypeWeighted signals that shared costs should be shared
	// proportionally, rather than evenly
	SplitTypeWeighted = "weighted"

	// UnallocatedSubfield indicates an allocation datum that does not have the
	// chosen Aggregator; e.g. during aggregation by some label, there may be
	// cost data that do not have the given label.
	UnallocatedSubfield = "__unallocated__"
)

// ParseAggregationProperties attempts to parse and return aggregation properties
// encoded under the given key. If none exist, or if parsing fails, an error
// is returned with empty AllocationProperties.
func ParseAggregationProperties(aggregations []string) ([]string, error) {
	aggregateBy := []string{}
	// In case of no aggregation option, aggregate to the container, with a key Cluster/Node/Namespace/Pod/Container
	if len(aggregations) == 0 {
		aggregateBy = []string{
			opencost.AllocationClusterProp,
			opencost.AllocationNodeProp,
			opencost.AllocationNamespaceProp,
			opencost.AllocationPodProp,
			opencost.AllocationContainerProp,
		}
	} else if len(aggregations) == 1 && aggregations[0] == "all" {
		aggregateBy = []string{}
	} else {
		for _, agg := range aggregations {
			aggregate := strings.TrimSpace(agg)
			if aggregate != "" {
				if prop, err := opencost.ParseProperty(aggregate); err == nil {
					aggregateBy = append(aggregateBy, string(prop))
				} else if strings.HasPrefix(aggregate, "label:") {
					aggregateBy = append(aggregateBy, aggregate)
				} else if strings.HasPrefix(aggregate, "annotation:") {
					aggregateBy = append(aggregateBy, aggregate)
				}
			}
		}
	}
	return aggregateBy, nil
}

func resolveAccumulateOption(accumulate opencost.AccumulateOption, accumulateBy string) (opencost.AccumulateOption, error) {
	accumulateByRaw := strings.TrimSpace(strings.ToLower(accumulateBy))
	if accumulateByRaw == "" {
		return accumulate, nil
	}

	if accumulateByRaw == "all" {
		return opencost.AccumulateOptionAll, nil
	}

	if accumulateByRaw == "none" {
		return opencost.AccumulateOptionNone, nil
	}

	accumulateByOpt := opencost.ParseAccumulate(accumulateByRaw)
	if accumulateByOpt == opencost.AccumulateOptionNone {
		return opencost.AccumulateOptionNone, fmt.Errorf("invalid accumulateBy option: %s", accumulateBy)
	}

	return accumulateByOpt, nil
}

func resolveAccumulateFromQuery(qp httputil.QueryParams) opencost.AccumulateOption {
	rawAccumulate := strings.TrimSpace(qp.Get("accumulate", ""))
	if strings.EqualFold(rawAccumulate, string(opencost.AccumulateOptionAll)) {
		return opencost.AccumulateOptionAll
	}

	accumulate := opencost.ParseAccumulate(rawAccumulate)
	if accumulate == opencost.AccumulateOptionNone && qp.GetBool("accumulate", false) {
		return opencost.AccumulateOptionAll
	}

	return accumulate
}

func resolveStepForAccumulate(step time.Duration, accumulateBy opencost.AccumulateOption) time.Duration {
	const (
		day  = 24 * time.Hour
		week = 7 * day
	)

	switch accumulateBy {
	case opencost.AccumulateOptionHour:
		return time.Hour
	case opencost.AccumulateOptionDay:
		// day accumulation supports either hourly or already-daily sets
		if step == day {
			return day
		}
		return time.Hour
	case opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth, opencost.AccumulateOptionQuarter:
		// week accumulation supports either daily or already-weekly sets
		if accumulateBy == opencost.AccumulateOptionWeek && step == week {
			return week
		}
		return day
	default:
		return step
	}
}

func resolveDefaultStepFromAccumulate(window opencost.Window, accumulateBy opencost.AccumulateOption) time.Duration {
	switch accumulateBy {
	case opencost.AccumulateOptionHour:
		return time.Hour
	case opencost.AccumulateOptionDay:
		return 24 * time.Hour
	case opencost.AccumulateOptionWeek:
		return 7 * 24 * time.Hour
	case opencost.AccumulateOptionMonth, opencost.AccumulateOptionQuarter:
		// month/quarter accumulation requires daily input sets
		return 24 * time.Hour
	case opencost.AccumulateOptionAll:
		return window.Duration()
	default:
		return window.Duration()
	}
}

func resolveStepFromQuery(qp httputil.QueryParams, window opencost.Window, accumulateBy opencost.AccumulateOption) (time.Duration, error) {
	stepRaw := strings.TrimSpace(strings.ToLower(qp.Get("step", "")))
	if stepRaw == "" {
		step := resolveDefaultStepFromAccumulate(window, accumulateBy)
		return resolveStepForAccumulate(step, accumulateBy), nil
	}

	switch stepRaw {
	case "hour":
		return resolveStepForAccumulate(time.Hour, accumulateBy), nil
	case "day":
		return resolveStepForAccumulate(24*time.Hour, accumulateBy), nil
	case "week":
		return resolveStepForAccumulate(7*24*time.Hour, accumulateBy), nil
	case "month":
		// month accumulation operates on daily inputs and calendar-rounded query windows
		return resolveStepForAccumulate(24*time.Hour, accumulateBy), nil
	case "quarter":
		// quarter accumulation operates on daily inputs and calendar-rounded query windows
		return resolveStepForAccumulate(24*time.Hour, accumulateBy), nil
	default:
		step, err := timeutil.ParseDuration(stepRaw)
		if err != nil {
			return 0, fmt.Errorf("invalid step %q: must be a Go duration or one of hour, day, week, month, quarter: %w", stepRaw, err)
		}
		return resolveStepForAccumulate(step, accumulateBy), nil
	}
}

func resolveQueryWindowForAccumulate(window opencost.Window, accumulateBy opencost.AccumulateOption) (opencost.Window, error) {
	switch accumulateBy {
	case opencost.AccumulateOptionHour, opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth, opencost.AccumulateOptionQuarter:
		windows, err := window.GetAccumulateWindows(accumulateBy)
		if err != nil {
			return opencost.Window{}, err
		}
		if len(windows) == 0 {
			return opencost.Window{}, fmt.Errorf("no query windows for accumulate option %s", accumulateBy)
		}

		return opencost.NewClosedWindow(*windows[0].Start(), *windows[len(windows)-1].End()), nil
	default:
		return window, nil
	}
}

func trimAllocationSetRangeToRequestWindow(asr *opencost.AllocationSetRange, requestWindow opencost.Window) *opencost.AllocationSetRange {
	if asr == nil {
		return nil
	}

	trimmed := opencost.NewAllocationSetRange()
	trimmed.FromStore = asr.FromStore
	for _, as := range asr.Allocations {
		// Keep only sets that overlap the originally requested window.
		if as.Start().Before(*requestWindow.End()) && as.End().After(*requestWindow.Start()) {
			trimmed.Append(as)
		}
	}

	return trimmed
}

// ComputeAllocationHandlerSummary computes a summarized AllocationSetRange.
// @Summary      查询成本分配摘要
// @Tags         Allocation
// @Description  返回成本分配摘要数据，支持窗口、聚合、过滤、累积和 CSV 导出。
// @Param        window        query  string  true   "时间窗口。必填。示例：window=7d、window=today、window=2026-05-01T00:00:00Z,2026-05-08T00:00:00Z"
// @Param        aggregate     query  string  false  "聚合维度，多个值使用英文逗号分隔。示例：aggregate=namespace,label:app"
// @Param        accumulate    query  string  false  "累计粒度。支持 true、all、hour、day、week、month。"
// @Param        accumulateBy  query  string  false  "显式累计粒度，覆盖 accumulate。"
// @Param        step          query  string  false  "查询步长。示例：step=1d"
// @Param        filter        query  string  false  "分配过滤条件。"
// @Param        format        query  string  false  "返回格式；传 csv 导出 CSV。"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /allocation/summary [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/allocation/summary [get]
// @Router       /allocation/compute/summary [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/allocation/compute/summary [get]
func (a *Accesses) ComputeAllocationHandlerSummary(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	qp := httputil.NewQueryParams(r.URL.Query())

	// CSV export: skip cache, compute and write CSV directly.
	if isCSVRequest(qp) {
		a.computeAllocationSummaryCSV(w, r, qp)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// Check query cache
	if resp, ok := a.getQueryCacheResponse("allocation-summary", r); ok {
		w.Write(resp)
		return
	}

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	// Step is an optional parameter that defines the duration per-set, i.e.
	// the window for an AllocationSet, of the AllocationSetRange to be
	// computed. Defaults to the window size, making one set.
	// Aggregation is a required comma-separated list of fields by which to
	// aggregate results. Some fields allow a sub-field, which is distinguished
	// with a colon; e.g. "label:app".
	// Examples: "namespace", "namespace,label:app"
	aggregations := qp.GetList("aggregate", ",")
	aggregateBy, err := ParseAggregationProperties(aggregations)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	// Accumulate is an optional parameter that accepts bool-style values (e.g.
	// true/1) or options (e.g. day/week/month) and governs accumulation windowing.
	accumulateOpt := resolveAccumulateFromQuery(qp)
	accumulateBy, err := resolveAccumulateOption(accumulateOpt, qp.Get("accumulateBy", ""))
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid 'accumulateBy' parameter: %s", err)))
		return
	}
	step, err := resolveStepFromQuery(qp, window, accumulateBy)
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid step parameter: %s", err)))
		return
	}
	queryWindow, err := resolveQueryWindowForAccumulate(window, accumulateBy)
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid accumulation configuration: %s", err)))
		return
	}

	// Get allocation filter if provided
	allocationFilter := qp.Get("filter", "")

	// Query for AllocationSets in increments of the given step duration,
	// appending each to the AllocationSetRange.
	asr := opencost.NewAllocationSetRange()
	stepStart := *queryWindow.Start()
	for queryWindow.End().After(stepStart) {
		stepEnd := stepStart.Add(step)
		stepWindow := opencost.NewWindow(&stepStart, &stepEnd)

		as, err := a.Model.ComputeAllocation(*stepWindow.Start(), *stepWindow.End())
		if err != nil {
			proto.WriteError(w, proto.InternalServerError(err.Error()))
			return
		}
		asr.Append(as)

		stepStart = stepEnd
	}

	// Apply allocation filter if provided
	if allocationFilter != "" {
		parser := allocation.NewAllocationFilterParser()
		filterNode, err := parser.Parse(allocationFilter)
		if err != nil {
			proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid filter: %s", err)))
			return
		}
		compiler := opencost.NewAllocationMatchCompiler(nil)
		matcher, err := compiler.Compile(filterNode)
		if err != nil {
			proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Failed to compile filter: %s", err)))
			return
		}
		filteredASR := opencost.NewAllocationSetRange()
		for _, as := range asr.Allocations {
			filteredAS := opencost.NewAllocationSet(as.Start(), as.End())
			for _, alloc := range as.Allocations {
				if matcher.Matches(alloc) {
					filteredAS.Set(alloc)
				}
			}
			if filteredAS.Length() > 0 {
				filteredASR.Append(filteredAS)
			}
		}
		asr = filteredASR
	}

	// Aggregate, if requested
	if len(aggregateBy) > 0 {
		err = asr.AggregateBy(aggregateBy, nil)
		if err != nil {
			proto.WriteError(w, proto.InternalServerError(err.Error()))
			return
		}
	}

	// Accumulate, if requested
	if accumulateBy != opencost.AccumulateOptionNone {
		asr, err = asr.Accumulate(accumulateBy)
		if err != nil {
			proto.WriteError(w, proto.InternalServerError(err.Error()))
			return
		}

		asr = trimAllocationSetRangeToRequestWindow(asr, window)
	}

	sasl := []*opencost.SummaryAllocationSet{}
	for _, as := range asr.Allocations {
		sas := opencost.NewSummaryAllocationSet(as, nil, nil, false, false)
		sasl = append(sasl, sas)
	}
	sasr := opencost.NewSummaryAllocationSetRange(sasl...)

	resp := WrapData(sasr, nil)
	a.setQueryCacheResponseWithTTL("allocation-summary", r, resp, cacheTTLForWindow(&window))
	w.Write(resp)
}

// ComputeAllocationHandlerClusterEfficiencySummary computes cluster-level
// efficiency summary from allocations.
// @Summary      查询集群效率摘要
// @Tags         Efficiency
// @Description  基于分配数据返回效率摘要。默认按集群聚合，支持按节点或自定义标签聚合。返回 groups + groupBy 结构；未显式传 step 时按日查询后累积为整个窗口的摘要；总成本包含 PV 和 idle，efficiency 与分配摘要页面口径一致。
// @Param        window      query  string  true   "时间窗口。必填。"
// @Param        step        query  string  false  "查询步长。未传时按日查询后累积为整个 window。"
// @Param        accumulate  query  bool    false  "是否将整个窗口累积为单个结果。"
// @Param        aggregate   query  string  false  "聚合维度，默认 cluster。支持 cluster、node、namespace、controllerKind、controller、pod、container、label:<key>、annotation:<key>。多个值使用英文逗号分隔。"
// @Param        filter      query  string  false  "分配过滤条件。"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /efficiency/clusters [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/efficiency/clusters [get]
// @Router       /efficiency/clusters/summary [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/efficiency/clusters/summary [get]
func (a *Accesses) ComputeAllocationHandlerClusterEfficiencySummary(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	step, accumulateBy, err := resolveClusterEfficiencyStepAndAccumulate(qp, window)
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid step parameter: %s", err)))
		return
	}
	aggregations := qp.GetList("aggregate", ",")
	aggregateBy := []string{opencost.AllocationClusterProp}
	if len(aggregations) > 0 {
		aggregateBy, err = ParseAggregationProperties(aggregations)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
			return
		}
	}

	// Query allocations aggregated by the requested grouping with filtering.
	asr, err := a.Model.QueryAllocation(window, step, aggregateBy, true, false, false, false, false, accumulateBy, false, qp.Get("filter", ""))
	if err != nil {
		proto.WriteError(w, proto.InternalServerError(err.Error()))
		return
	}

	sasl := []*opencost.SummaryAllocationSet{}
	for _, as := range asr.Allocations {
		sas := opencost.NewSummaryAllocationSet(as, nil, nil, false, false)
		sasl = append(sasl, sas)
	}
	sasr := opencost.NewSummaryAllocationSetRange(sasl...)

	proto.WriteData(w, sasr.ClusterEfficiencySetRange(aggregateBy))
}

func resolveClusterEfficiencyStepAndAccumulate(qp httputil.QueryParams, window opencost.Window) (time.Duration, opencost.AccumulateOption, error) {
	accumulateBy := opencost.AccumulateOptionNone
	if qp.GetBool("accumulate", false) {
		accumulateBy = opencost.AccumulateOptionAll
	}

	stepRaw := strings.TrimSpace(qp.Get("step", ""))
	step, err := resolveStepFromQuery(qp, window, accumulateBy)
	if err != nil {
		return 0, "", err
	}

	if stepRaw == "" && window.Duration() > 24*time.Hour {
		return 24 * time.Hour, opencost.AccumulateOptionAll, nil
	}

	return step, accumulateBy, nil
}

type SummaryAllocationToplineResponse struct {
	NumResults int                                  `json:"numResults"`
	Combined   *SummaryAllocationSetToplineResponse `json:"combined"`
}

type SummaryAllocationSetToplineResponse struct {
	Allocations map[string]*SummaryAllocationToplineItem `json:"allocations"`
	Window      opencost.Window                          `json:"window"`
}

type SummaryAllocationToplineItem struct {
	Name                   string    `json:"name"`
	Start                  time.Time `json:"start"`
	End                    time.Time `json:"end"`
	CPUCoreRequestAverage  float64   `json:"cpuCoreRequestAverage"`
	CPUCoreUsageAverage    float64   `json:"cpuCoreUsageAverage"`
	CPUCost                float64   `json:"cpuCost"`
	CPUCostIdle            float64   `json:"cpuCostIdle"`
	GPURequestAverage      float64   `json:"gpuRequestAverage"`
	GPUUsageAverage        float64   `json:"gpuUsageAverage"`
	GPUCost                float64   `json:"gpuCost"`
	GPUCostIdle            float64   `json:"gpuCostIdle"`
	NetworkCost            float64   `json:"networkCost"`
	LoadBalancerCost       float64   `json:"loadBalancerCost"`
	PVCost                 float64   `json:"pvCost"`
	RAMBytesRequestAverage float64   `json:"ramByteRequestAverage"`
	RAMBytesUsageAverage   float64   `json:"ramByteUsageAverage"`
	RAMCost                float64   `json:"ramCost"`
	RAMCostIdle            float64   `json:"ramCostIdle"`
	SharedCost             float64   `json:"sharedCost"`
	ExternalCost           float64   `json:"externalCost"`
	Efficiency             float64   `json:"efficiency"`
}

// ComputeAllocationHandlerSummaryTopline
// @Summary      查询成本分配 topline 摘要
// @Tags         Allocation
// @Description  兼容 Kubecost allocation/summary/topline 的概览接口，返回整个查询窗口的 combined total 摘要。
// @Description  适合顶部卡片、总览页和趋势页头部指标。返回结构固定为 numResults + combined.allocations.total。
// @Description  当前仅支持 window、aggregate、filter、idle、idleByNode、shareIdle、accumulate、step、resolution。
// @Param        window                     query  string  true   "时间窗口。必填。示例：window=7d、window=1d、window=2026-05-01T00:00:00Z,2026-05-08T00:00:00Z"
// @Param        aggregate                  query  string  false  "聚合维度。支持 namespace、cluster、node、controllerKind、controller、pod、container、label:<key>、annotation:<key>。示例：aggregate=namespace"
// @Param        filter                     query  string  false  "分配过滤条件。语法与 /allocation 保持一致。示例：filter=cluster:%22host%22%2Bnamespace:%22default%22"
// @Param        resolution                 query  string  false  "Prometheus 查询分辨率。默认 1m。示例：resolution=5m"
// @Param        step                       query  string  false  "查询步长。默认等于整个 window。示例：step=1d"
// @Param        accumulate                 query  string  false  "累计粒度。支持：true、all、hour、day、week、month。该接口最终始终返回整个窗口的 combined total，但会按该粒度先组织分配集合。示例：accumulate=day"
// @Param        idle                       query  bool    false  "是否包含 idle 成本。示例：idle=true"
// @Param        idleByNode                 query  bool    false  "是否按节点级别计算 idle。仅在 idle=true 时有意义。示例：idleByNode=true"
// @Param        shareIdle                  query  bool    false  "是否将 idle 成本按规则分摊回工作负载。示例：shareIdle=true"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /allocation/summary/topline [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/allocation/summary/topline [get]
func (a *Accesses) ComputeAllocationHandlerSummaryTopline(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	step := qp.GetDuration("step", window.Duration())

	aggregations := qp.GetList("aggregate", ",")
	aggregateBy, err := ParseAggregationProperties(aggregations)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	includeIdle := qp.GetBool("idle", qp.GetBool("includeIdle", false))
	idleByNode := qp.GetBool("idleByNode", false)
	shareIdle := qp.GetBool("shareIdle", false)
	accumulateBy := opencost.ParseAccumulate(qp.Get("accumulate", ""))
	filterString := qp.Get("filter", "")

	asr, err := a.Model.QueryAllocation(window, step, aggregateBy, includeIdle, idleByNode, false, false, false, accumulateBy, shareIdle, filterString)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "bad request") {
			proto.WriteError(w, proto.BadRequest(err.Error()))
		} else {
			proto.WriteError(w, proto.InternalServerError(err.Error()))
		}
		return
	}

	sasl := make([]*opencost.SummaryAllocationSet, 0, len(asr.Allocations))
	for _, as := range asr.Allocations {
		sas := opencost.NewSummaryAllocationSet(as, nil, nil, false, false)
		if sas != nil {
			sasl = append(sasl, sas)
		}
	}

	resp, err := buildSummaryAllocationToplineResponse(opencost.NewSummaryAllocationSetRange(sasl...))
	if err != nil {
		proto.WriteError(w, proto.InternalServerError(err.Error()))
		return
	}

	w.Write(WrapData(resp, nil))
}

func buildSummaryAllocationToplineResponse(sasr *opencost.SummaryAllocationSetRange) (*SummaryAllocationToplineResponse, error) {
	if sasr == nil {
		return &SummaryAllocationToplineResponse{
			Combined: &SummaryAllocationSetToplineResponse{
				Allocations: map[string]*SummaryAllocationToplineItem{},
				Window:      opencost.NewWindow(nil, nil),
			},
		}, nil
	}

	total, numResults := summarizeSummaryAllocationSetRange(sasr)
	if total == nil {
		return &SummaryAllocationToplineResponse{
			Combined: &SummaryAllocationSetToplineResponse{
				Allocations: map[string]*SummaryAllocationToplineItem{},
				Window:      sasr.Window.Clone(),
			},
		}, nil
	}

	allocations := map[string]*SummaryAllocationToplineItem{}
	allocations["total"] = summaryAllocationToToplineItem(total)

	return &SummaryAllocationToplineResponse{
		NumResults: numResults,
		Combined: &SummaryAllocationSetToplineResponse{
			Allocations: allocations,
			Window:      sasr.Window.Clone(),
		},
	}, nil
}

func summarizeSummaryAllocationSetRange(sasr *opencost.SummaryAllocationSetRange) (*opencost.SummaryAllocation, int) {
	if sasr == nil {
		return nil, 0
	}

	var total *opencost.SummaryAllocation
	numResults := 0

	for _, sas := range sasr.SummaryAllocationSets {
		if sas == nil {
			continue
		}

		numResults += len(sas.SummaryAllocations)
		setTotal := summarizeSummaryAllocationSet(sas)
		if setTotal == nil {
			continue
		}

		if total == nil {
			total = setTotal
			total.Name = "total"
			continue
		}

		_ = total.Add(setTotal)
		total.CPUCostIdle += setTotal.CPUCostIdle
		total.GPUCostIdle += setTotal.GPUCostIdle
		total.RAMCostIdle += setTotal.RAMCostIdle
	}

	return total, numResults
}

func summarizeSummaryAllocationSet(sas *opencost.SummaryAllocationSet) *opencost.SummaryAllocation {
	if sas == nil {
		return nil
	}

	var total *opencost.SummaryAllocation
	for _, sa := range sas.SummaryAllocations {
		if sa == nil {
			continue
		}

		if total == nil {
			total = &opencost.SummaryAllocation{
				Name:                   "total",
				Start:                  sa.Start,
				End:                    sa.End,
				CPUCoreRequestAverage:  sa.CPUCoreRequestAverage,
				CPUCoreUsageAverage:    sa.CPUCoreUsageAverage,
				CPUCost:                sa.CPUCost,
				CPUCostIdle:            sa.CPUCostIdle,
				GPURequestAverage:      sa.GPURequestAverage,
				GPUUsageAverage:        sa.GPUUsageAverage,
				GPUCost:                sa.GPUCost,
				GPUCostIdle:            sa.GPUCostIdle,
				NetworkCost:            sa.NetworkCost,
				LoadBalancerCost:       sa.LoadBalancerCost,
				PVCost:                 sa.PVCost,
				RAMBytesRequestAverage: sa.RAMBytesRequestAverage,
				RAMBytesUsageAverage:   sa.RAMBytesUsageAverage,
				RAMCost:                sa.RAMCost,
				RAMCostIdle:            sa.RAMCostIdle,
				SharedCost:             sa.SharedCost,
				ExternalCost:           sa.ExternalCost,
				Efficiency:             sa.Efficiency,
			}
			continue
		}

		_ = total.Add(sa)
		total.CPUCostIdle += sa.CPUCostIdle
		total.GPUCostIdle += sa.GPUCostIdle
		total.RAMCostIdle += sa.RAMCostIdle
	}

	return total
}

func summaryAllocationToToplineItem(sa *opencost.SummaryAllocation) *SummaryAllocationToplineItem {
	if sa == nil {
		return nil
	}

	var gpuRequestAverage float64
	if sa.GPURequestAverage != nil {
		gpuRequestAverage = *sa.GPURequestAverage
	}

	var gpuUsageAverage float64
	if sa.GPUUsageAverage != nil {
		gpuUsageAverage = *sa.GPUUsageAverage
	}

	return &SummaryAllocationToplineItem{
		Name:                   sa.Name,
		Start:                  sa.Start,
		End:                    sa.End,
		CPUCoreRequestAverage:  sa.CPUCoreRequestAverage,
		CPUCoreUsageAverage:    sa.CPUCoreUsageAverage,
		CPUCost:                sa.CPUCost,
		CPUCostIdle:            sa.CPUCostIdle,
		GPURequestAverage:      gpuRequestAverage,
		GPUUsageAverage:        gpuUsageAverage,
		GPUCost:                sa.GPUCost,
		GPUCostIdle:            sa.GPUCostIdle,
		NetworkCost:            sa.NetworkCost,
		LoadBalancerCost:       sa.LoadBalancerCost,
		PVCost:                 sa.PVCost,
		RAMBytesRequestAverage: sa.RAMBytesRequestAverage,
		RAMBytesUsageAverage:   sa.RAMBytesUsageAverage,
		RAMCost:                sa.RAMCost,
		RAMCostIdle:            sa.RAMCostIdle,
		SharedCost:             sa.SharedCost,
		ExternalCost:           sa.ExternalCost,
		Efficiency:             sa.Efficiency,
	}
}

// ComputeAllocationHandler computes an AllocationSetRange from the CostModel.
// @Summary      查询成本分配明细
// @Tags         Allocation
// @Description  返回成本分配明细数据，支持聚合、过滤、idle、共享 idle、比例资产成本和 CSV 导出。
// @Param        window                           query  string  true   "时间窗口。必填。"
// @Param        aggregate                        query  string  false  "聚合维度，多个值使用英文逗号分隔。"
// @Param        includeIdle                      query  bool    false  "是否包含 idle 成本。"
// @Param        idleByNode                       query  bool    false  "是否按节点级别计算 idle。"
// @Param        sharelb                          query  bool    false  "是否共享负载均衡成本。"
// @Param        includeProportionalAssetResourceCosts  query  bool  false  "是否纳入比例资产资源成本。"
// @Param        includeAggregatedMetadata        query  bool    false  "是否附带聚合元数据。"
// @Param        shareIdle                        query  bool    false  "是否回摊 idle 成本。"
// @Param        accumulate                       query  string  false  "累计粒度。"
// @Param        accumulateBy                     query  string  false  "显式累计粒度，覆盖 accumulate。"
// @Param        step                             query  string  false  "查询步长。"
// @Param        filter                           query  string  false  "分配过滤条件。"
// @Param        format                           query  string  false  "返回格式；传 csv 导出 CSV。"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /allocation [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/allocation [get]
// @Router       /allocation/compute [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/allocation/compute [get]
func (a *Accesses) ComputeAllocationHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	qp := httputil.NewQueryParams(r.URL.Query())

	// CSV export: skip cache, compute and write CSV directly.
	if isCSVRequest(qp) {
		a.computeAllocationCSV(w, r, qp)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// Check query cache
	if resp, ok := a.getQueryCacheResponse("allocation", r); ok {
		w.Write(resp)
		return
	}

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	// Step is an optional parameter that defines the duration per-set, i.e.
	// the window for an AllocationSet, of the AllocationSetRange to be
	// computed. Defaults to the window size, making one set.
	// Aggregation is an optional comma-separated list of fields by which to
	// aggregate results. Some fields allow a sub-field, which is distinguished
	// with a colon; e.g. "label:app".
	// Examples: "namespace", "namespace,label:app"
	aggregations := qp.GetList("aggregate", ",")
	aggregateBy, err := ParseAggregationProperties(aggregations)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	// IncludeIdle, if true, uses Asset data to incorporate Idle Allocation
	includeIdle := qp.GetBool("includeIdle", false)
	// Accumulate is an optional parameter that accepts bool-style values (e.g.
	// true/1) or options (e.g. day/week/month) and governs accumulation windowing.
	accumulateOpt := resolveAccumulateFromQuery(qp)

	// AccumulateBy is an optional parameter that overrides accumulate with an
	// explicit accumulation option (e.g. all/day/week/month/quarter/none).
	accumulateBy, err := resolveAccumulateOption(accumulateOpt, qp.Get("accumulateBy", ""))
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid 'accumulateBy' parameter: %s", err)))
		return
	}
	step, err := resolveStepFromQuery(qp, window, accumulateBy)
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid step parameter: %s", err)))
		return
	}

	// IdleByNode, if true, computes idle allocations at the node level.
	// Otherwise it is computed at the cluster level. (Not relevant if idle
	// is not included.)
	idleByNode := qp.GetBool("idleByNode", false)
	sharedLoadBalancer := qp.GetBool("sharelb", false)

	// IncludeProportionalAssetResourceCosts, if true,
	includeProportionalAssetResourceCosts := qp.GetBool("includeProportionalAssetResourceCosts", false)

	// include aggregated labels/annotations if true
	includeAggregatedMetadata := qp.GetBool("includeAggregatedMetadata", false)

	shareIdle := qp.GetBool("shareIdle", false)

	// Get allocation filter if provided
	allocationFilter := qp.Get("filter", "")

	// Query allocations with filtering, aggregation, and accumulation.
	// Filtering is done BEFORE aggregation inside QueryAllocation to ensure
	// filters can match on all allocation properties (like cluster, node, etc.)
	// before they are potentially lost or merged during aggregation.
	asr, err := a.Model.QueryAllocation(window, step, aggregateBy, includeIdle, idleByNode, includeProportionalAssetResourceCosts, includeAggregatedMetadata, sharedLoadBalancer, accumulateBy, shareIdle, allocationFilter)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "bad request") {
			proto.WriteError(w, proto.BadRequest(err.Error()))
		} else {
			proto.WriteError(w, proto.InternalServerError(err.Error()))
		}

		return
	}

	resp := WrapData(asr, nil)
	a.setQueryCacheResponseWithTTL("allocation", r, resp, cacheTTLForWindow(&window))
	w.Write(resp)
}

// computeAllocationCSV handles the CSV export path for /allocation/compute.
// It mirrors the JSON handler's query logic but writes CSV output instead.
func (a *Accesses) computeAllocationCSV(w http.ResponseWriter, r *http.Request, qp httputil.QueryParams) {
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	aggregations := qp.GetList("aggregate", ",")
	aggregateBy, err := ParseAggregationProperties(aggregations)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	includeIdle := qp.GetBool("includeIdle", false)
	accumulateOpt := resolveAccumulateFromQuery(qp)
	accumulateBy, err := resolveAccumulateOption(accumulateOpt, qp.Get("accumulateBy", ""))
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid 'accumulateBy' parameter: %s", err)))
		return
	}
	step, err := resolveStepFromQuery(qp, window, accumulateBy)
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid step parameter: %s", err)))
		return
	}

	idleByNode := qp.GetBool("idleByNode", false)
	sharedLoadBalancer := qp.GetBool("sharelb", false)
	includeProportionalAssetResourceCosts := qp.GetBool("includeProportionalAssetResourceCosts", false)
	includeAggregatedMetadata := qp.GetBool("includeAggregatedMetadata", false)
	shareIdle := qp.GetBool("shareIdle", false)
	allocationFilter := qp.Get("filter", "")

	asr, err := a.Model.QueryAllocation(window, step, aggregateBy, includeIdle, idleByNode, includeProportionalAssetResourceCosts, includeAggregatedMetadata, sharedLoadBalancer, accumulateBy, shareIdle, allocationFilter)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "bad request") {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	filename := buildCSVFilename("allocation", window, qp.Get("aggregate", ""))

	var buf bytes.Buffer
	truncated, err := writeAllocationComputeCSV(&buf, asr, 10000)
	if err != nil {
		log.Errorf("failed to write allocation CSV: %v", err)
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

// computeAllocationSummaryCSV handles the CSV export path for
// /allocation/summary. It mirrors the summary handler's query logic but writes
// CSV output instead of JSON.
func (a *Accesses) computeAllocationSummaryCSV(w http.ResponseWriter, r *http.Request, qp httputil.QueryParams) {
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	aggregations := qp.GetList("aggregate", ",")
	aggregateBy, err := ParseAggregationProperties(aggregations)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	accumulateOpt := resolveAccumulateFromQuery(qp)
	accumulateBy, err := resolveAccumulateOption(accumulateOpt, qp.Get("accumulateBy", ""))
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'accumulateBy' parameter: %s", err), http.StatusBadRequest)
		return
	}
	step, err := resolveStepFromQuery(qp, window, accumulateBy)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid step parameter: %s", err), http.StatusBadRequest)
		return
	}
	queryWindow, err := resolveQueryWindowForAccumulate(window, accumulateBy)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid accumulation configuration: %s", err), http.StatusBadRequest)
		return
	}

	allocationFilter := qp.Get("filter", "")

	// Query for AllocationSets in increments of the given step duration
	asr := opencost.NewAllocationSetRange()
	stepStart := *queryWindow.Start()
	for queryWindow.End().After(stepStart) {
		stepEnd := stepStart.Add(step)
		stepWindow := opencost.NewWindow(&stepStart, &stepEnd)

		as, err := a.Model.ComputeAllocation(*stepWindow.Start(), *stepWindow.End())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		asr.Append(as)

		stepStart = stepEnd
	}

	// Apply allocation filter if provided
	if allocationFilter != "" {
		parser := allocation.NewAllocationFilterParser()
		filterNode, err := parser.Parse(allocationFilter)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid filter: %s", err), http.StatusBadRequest)
			return
		}
		compiler := opencost.NewAllocationMatchCompiler(nil)
		matcher, err := compiler.Compile(filterNode)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to compile filter: %s", err), http.StatusBadRequest)
			return
		}
		filteredASR := opencost.NewAllocationSetRange()
		for _, as := range asr.Allocations {
			filteredAS := opencost.NewAllocationSet(as.Start(), as.End())
			for _, alloc := range as.Allocations {
				if matcher.Matches(alloc) {
					filteredAS.Set(alloc)
				}
			}
			if filteredAS.Length() > 0 {
				filteredASR.Append(filteredAS)
			}
		}
		asr = filteredASR
	}

	// Aggregate, if requested
	if len(aggregateBy) > 0 {
		err = asr.AggregateBy(aggregateBy, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Accumulate, if requested
	if accumulateBy != opencost.AccumulateOptionNone {
		asr, err = asr.Accumulate(accumulateBy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		asr = trimAllocationSetRangeToRequestWindow(asr, window)
	}

	sasl := []*opencost.SummaryAllocationSet{}
	for _, as := range asr.Allocations {
		sas := opencost.NewSummaryAllocationSet(as, nil, nil, false, false)
		sasl = append(sasl, sas)
	}
	sasr := opencost.NewSummaryAllocationSetRange(sasl...)

	filename := buildCSVFilename("allocation-summary", window, qp.Get("aggregate", ""))

	var buf bytes.Buffer
	truncated, err := writeAllocationSummaryCSV(&buf, sasr, 10000)
	if err != nil {
		log.Errorf("failed to write allocation summary CSV: %v", err)
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
