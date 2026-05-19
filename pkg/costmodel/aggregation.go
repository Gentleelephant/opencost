package costmodel

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"

	"github.com/opencost/opencost/core/pkg/filter/allocation"
	"github.com/opencost/opencost/core/pkg/filter/ast"
	"github.com/opencost/opencost/core/pkg/filter/matcher"
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

func buildAllocationFilter(filterString string) (opencost.AllocationMatcher, error) {
	if filterString == "" {
		return &matcher.AllPass[*opencost.Allocation]{}, nil
	}

	filterString = normalizeAllocationFilterString(filterString)

	parser := allocation.NewAllocationFilterParser()
	tree, err := parser.Parse(filterString)
	if err != nil {
		return nil, fmt.Errorf("err parsing filter '%s': %v", ast.ToPreOrderShortString(tree), err)
	}

	compiler := opencost.NewAllocationMatchCompiler(nil)
	filter, err := compiler.Compile(tree)
	if err != nil {
		return nil, fmt.Errorf("err compiling filter '%s': %v", ast.ToPreOrderShortString(tree), err)
	}
	if filter == nil {
		return nil, fmt.Errorf("unexpected nil filter")
	}

	return filter, nil
}

func normalizeAllocationFilterString(filter string) string {
	var builder strings.Builder
	builder.Grow(len(filter) + 8)

	inQuotes := false
	lastSig := byte(0)

	for i := 0; i < len(filter); {
		ch := filter[i]
		if ch == '"' {
			inQuotes = !inQuotes
			builder.WriteByte(ch)
			lastSig = ch
			i++
			continue
		}
		if inQuotes {
			builder.WriteByte(ch)
			lastSig = ch
			i++
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			j := i + 1
			for j < len(filter) {
				next := filter[j]
				if next != ' ' && next != '\t' && next != '\n' && next != '\r' {
					break
				}
				j++
			}
			var nextSig byte
			if j < len(filter) {
				nextSig = filter[j]
			}
			if isImplicitAllocationAndBoundary(lastSig, nextSig) {
				builder.WriteString(" + ")
				lastSig = '+'
			}
			i = j
			continue
		}
		builder.WriteByte(ch)
		lastSig = ch
		i++
	}

	return builder.String()
}

func isImplicitAllocationAndBoundary(prev, next byte) bool {
	return isAllocationFilterExprEnd(prev) && isAllocationFilterExprStart(next)
}

func isAllocationFilterExprEnd(ch byte) bool {
	return ch == '"' || ch == ')'
}

func isAllocationFilterExprStart(ch byte) bool {
	return ch == '(' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}

func (a *Accesses) ComputeAllocationHandlerSummary(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	if resp, ok := a.getQueryCacheResponse("allocation-summary", r); ok {
		w.Write(resp)
		return
	}

	qp := httputil.NewQueryParams(r.URL.Query())

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
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
		allocationMatcher, err := buildAllocationFilter(allocationFilter)
		if err != nil {
			proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid filter: %s", err)))
			return
		}
		filteredASR := opencost.NewAllocationSetRange()
		for _, as := range asr.Allocations {
			filteredAS := opencost.NewAllocationSet(as.Start(), as.End())
			for _, alloc := range as.Allocations {
				if allocationMatcher.Matches(alloc) {
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

	resp := WrapData(sasr.ToResponse(), nil)
	a.setQueryCacheResponse("allocation-summary", r, resp)
	w.Write(resp)
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

func (a *Accesses) ComputeAllocationHandlerSummaryTopline(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

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

	aggregateBy, err := ParseAggregationProperties(qp.GetList("aggregate", ","))
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	includeIdle := qp.GetBool("idle", qp.GetBool("includeIdle", false))
	idleByNode := qp.GetBool("idleByNode", false)
	shareIdle := qp.GetBool("shareIdle", false)

	asr, err := a.Model.QueryAllocation(window, step, aggregateBy, includeIdle, idleByNode, false, false, false, accumulateBy, shareIdle, qp.Get("filter", ""))
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
		sasl = append(sasl, opencost.NewSummaryAllocationSet(as, nil, nil, false, false))
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

	return &SummaryAllocationToplineResponse{
		NumResults: numResults,
		Combined: &SummaryAllocationSetToplineResponse{
			Allocations: map[string]*SummaryAllocationToplineItem{
				"total": summaryAllocationToToplineItem(total),
			},
			Window: sasr.Window.Clone(),
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

func (a *Accesses) ComputeAllocationHandlerClusterEfficiencySummary(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	accumulateBy := opencost.AccumulateOptionNone
	if qp.GetBool("accumulate", false) {
		accumulateBy = opencost.AccumulateOptionAll
	}
	step, err := resolveStepFromQuery(qp, window, accumulateBy)
	if err != nil {
		proto.WriteError(w, proto.BadRequest(fmt.Sprintf("Invalid step parameter: %s", err)))
		return
	}

	asr, err := a.Model.QueryAllocation(window, step, nil, true, false, false, false, false, accumulateBy, false, qp.Get("filter", ""))
	if err != nil {
		proto.WriteError(w, proto.InternalServerError(err.Error()))
		return
	}

	sasl := make([]*opencost.SummaryAllocationSet, 0, len(asr.Slice()))
	for _, as := range asr.Slice() {
		sas := opencost.NewSummaryAllocationSet(as, nil, nil, false, false)
		if err := sas.AggregateBy([]string{opencost.AllocationClusterProp}, &opencost.AllocationAggregationOptions{
			ShareIdle: opencost.ShareNone,
			SplitIdle: true,
		}); err != nil {
			proto.WriteError(w, proto.InternalServerError(err.Error()))
			return
		}
		sasl = append(sasl, sas)
	}

	w.Write(WrapData(opencost.NewSummaryAllocationSetRange(sasl...).ClusterEfficiencySetRange(), nil))
}

// ComputeAllocationHandler computes an AllocationSetRange from the CostModel.
func (a *Accesses) ComputeAllocationHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	if resp, ok := a.getQueryCacheResponse("allocation", r); ok {
		w.Write(resp)
		return
	}

	qp := httputil.NewQueryParams(r.URL.Query())

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
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
	a.setQueryCacheResponse("allocation", r, resp)
	w.Write(resp)
}
