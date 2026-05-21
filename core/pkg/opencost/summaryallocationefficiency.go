package opencost

import (
	"math"
	"time"
)

// ResourceCostBreakdown breaks a single resource type into allocation/usage/idle.
type ResourceCostBreakdown struct {
	Allocation float64 `json:"allocation"`
	Usage      float64 `json:"usage"`
	Idle       float64 `json:"idle"`
	Efficiency float64 `json:"efficiency"`
}

type ClusterEfficiency struct {
	Name                   string                 `json:"name"`
	Start                  time.Time              `json:"start"`
	End                    time.Time              `json:"end"`
	CPUEfficiency          float64                `json:"cpuEfficiency"`
	RAMEfficiency          float64                `json:"ramEfficiency"`
	GPUEfficiency          float64                `json:"gpuEfficiency"`
	TotalUsageCost         float64                `json:"totalUsageCost"`
	WorkloadAllocationCost float64                `json:"workloadAllocationCost"`
	WorkloadIdleCost       float64                `json:"workloadIdleCost"`
	InfraIdleCost          float64                `json:"infraIdleCost"`
	TotalIdleCost          float64                `json:"totalIdleCost"`
	TotalCost              float64                `json:"totalCost"`
	Efficiency             float64                `json:"efficiency"`
	CPU                    *ResourceCostBreakdown `json:"cpu,omitempty"`
	RAM                    *ResourceCostBreakdown `json:"ram,omitempty"`
	GPU                    *ResourceCostBreakdown `json:"gpu,omitempty"`
	PV                     *ResourceCostBreakdown `json:"pv,omitempty"`
}

type ClusterEfficiencySet struct {
	GroupBy []string                      `json:"groupBy"`
	Groups  map[string]*ClusterEfficiency `json:"groups"`
	Summary *ClusterEfficiency            `json:"summary"`
	Window  Window                        `json:"window"`
}

type ClusterEfficiencySetRange struct {
	Step   time.Duration           `json:"step"`
	Sets   []*ClusterEfficiencySet `json:"sets"`
	Window Window                  `json:"window"`
}

func clampEfficiency(value float64) float64 {
	return math.Min(1.0, math.Max(0.0, value))
}

func kubecostEfficiency(totalUsageCost, costBasis float64) float64 {
	if costBasis <= 0 {
		return 0.0
	}

	return clampEfficiency(totalUsageCost / costBasis)
}

func pageEfficiency(usage, request, cost float64) float64 {
	if request > 0 {
		return usage / request
	}

	if usage > 0 || cost > 0 {
		return 1.0
	}

	return 0.0
}

func ptrFloat64Value(value *float64) float64 {
	if value == nil {
		return 0.0
	}

	return *value
}

func (sa *SummaryAllocation) ClusterCPUEfficiency() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return sa.CPUEfficiency()
}

func (sa *SummaryAllocation) ClusterRAMEfficiency() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return sa.RAMEfficiency()
}

func (sa *SummaryAllocation) ClusterGPUEfficiency() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return pageEfficiency(ptrFloat64Value(sa.GPUUsageAverage), ptrFloat64Value(sa.GPURequestAverage), sa.GPUCost)
}

func workloadAllocationCost(cost, embeddedIdle float64) float64 {
	return math.Max(0.0, cost-embeddedIdle)
}

func resourceBreakdown(totalAllocation, workloadAllocation, efficiency float64) *ResourceCostBreakdown {
	usage := efficiency * workloadAllocation
	return &ResourceCostBreakdown{
		Allocation: totalAllocation,
		Usage:      usage,
		Idle:       totalAllocation - usage,
		Efficiency: efficiency,
	}
}

type efficiencyTotals struct {
	totalUsageCost              float64
	totalWorkloadAllocationCost float64
	totalWorkloadIdleCost       float64
	totalInfraIdleCost          float64
	totalCost                   float64
	cpuAlloc                    float64
	cpuUsage                    float64
	cpuIdle                     float64
	ramAlloc                    float64
	ramUsage                    float64
	ramIdle                     float64
	gpuAlloc                    float64
	gpuUsage                    float64
	gpuIdle                     float64
	pvAlloc                     float64
	pvUsage                     float64
	pvIdle                      float64
}

func (et *efficiencyTotals) addSummaryAllocation(sa *SummaryAllocation) {
	if sa == nil || sa.IsExternal() || sa.IsUnallocated() || sa.IsUnmounted() {
		return
	}

	if sa.IsIdle() {
		idleCost := sa.CPUCost + sa.RAMCost + sa.GPUCost
		et.totalInfraIdleCost += idleCost
		et.totalCost += idleCost + sa.PVCost + sa.NetworkCost + sa.LoadBalancerCost + sa.SharedCost + sa.ExternalCost
		et.cpuAlloc += sa.CPUCost
		et.cpuIdle += sa.CPUCost
		et.ramAlloc += sa.RAMCost
		et.ramIdle += sa.RAMCost
		et.gpuAlloc += sa.GPUCost
		et.gpuIdle += sa.GPUCost
		if sa.PVCost > 0 {
			et.pvAlloc += sa.PVCost
			et.pvUsage += sa.PVCost
		}
		return
	}

	metric := sa.ClusterEfficiencyMetric()
	if metric == nil {
		return
	}

	et.totalUsageCost += metric.TotalUsageCost
	et.totalWorkloadAllocationCost += metric.WorkloadAllocationCost
	et.totalWorkloadIdleCost += metric.WorkloadIdleCost
	et.totalInfraIdleCost += metric.InfraIdleCost
	et.totalCost += metric.TotalCost

	if metric.CPU != nil {
		et.cpuAlloc += metric.CPU.Allocation
		et.cpuUsage += metric.CPU.Usage
		et.cpuIdle += metric.CPU.Idle
	}
	if metric.RAM != nil {
		et.ramAlloc += metric.RAM.Allocation
		et.ramUsage += metric.RAM.Usage
		et.ramIdle += metric.RAM.Idle
	}
	if metric.GPU != nil {
		et.gpuAlloc += metric.GPU.Allocation
		et.gpuUsage += metric.GPU.Usage
		et.gpuIdle += metric.GPU.Idle
	}
	if metric.PV != nil {
		et.pvAlloc += metric.PV.Allocation
		et.pvUsage += metric.PV.Usage
		et.pvIdle += metric.PV.Idle
	}
}

func buildSummaryMetric(summaryAllocations map[string]*SummaryAllocation, window Window) *ClusterEfficiency {
	totals := &efficiencyTotals{}
	for _, sa := range summaryAllocations {
		totals.addSummaryAllocation(sa)
	}

	summaryCPU := &ResourceCostBreakdown{Allocation: totals.cpuAlloc, Usage: totals.cpuUsage, Idle: totals.cpuIdle}
	if totals.cpuAlloc > 0 {
		summaryCPU.Efficiency = totals.cpuUsage / totals.cpuAlloc
	}
	summaryRAM := &ResourceCostBreakdown{Allocation: totals.ramAlloc, Usage: totals.ramUsage, Idle: totals.ramIdle}
	if totals.ramAlloc > 0 {
		summaryRAM.Efficiency = totals.ramUsage / totals.ramAlloc
	}
	summaryGPU := &ResourceCostBreakdown{Allocation: totals.gpuAlloc, Usage: totals.gpuUsage, Idle: totals.gpuIdle}
	if totals.gpuAlloc > 0 {
		summaryGPU.Efficiency = totals.gpuUsage / totals.gpuAlloc
	}

	var summaryPV *ResourceCostBreakdown
	if totals.pvAlloc > 0 {
		summaryPV = &ResourceCostBreakdown{Allocation: totals.pvAlloc, Usage: totals.pvUsage, Idle: totals.pvIdle}
		summaryPV.Efficiency = totals.pvUsage / totals.pvAlloc
	}

	summary := &ClusterEfficiency{
		Name:                   "summary",
		TotalUsageCost:         totals.totalUsageCost,
		WorkloadAllocationCost: totals.totalWorkloadAllocationCost,
		WorkloadIdleCost:       totals.totalWorkloadIdleCost,
		InfraIdleCost:          totals.totalInfraIdleCost,
		TotalIdleCost:          totals.totalWorkloadIdleCost + totals.totalInfraIdleCost,
		TotalCost:              totals.totalCost,
		CPU:                    summaryCPU,
		RAM:                    summaryRAM,
		GPU:                    summaryGPU,
		PV:                     summaryPV,
		Efficiency:             kubecostEfficiency(totals.totalUsageCost, totals.totalWorkloadAllocationCost),
	}

	if window.Start() != nil {
		summary.Start = *window.Start()
	}
	if window.End() != nil {
		summary.End = *window.End()
	}

	return summary
}

func (sa *SummaryAllocation) ClusterEfficiencyMetric() *ClusterEfficiency {
	if sa == nil {
		return nil
	}

	cpuEff := sa.ClusterCPUEfficiency()
	ramEff := sa.ClusterRAMEfficiency()
	gpuEff := sa.ClusterGPUEfficiency()

	workloadCPUCost := workloadAllocationCost(sa.CPUCost, sa.CPUCostIdle)
	workloadRAMCost := workloadAllocationCost(sa.RAMCost, sa.RAMCostIdle)
	workloadGPUCost := workloadAllocationCost(sa.GPUCost, sa.GPUCostIdle)
	workloadAllocationCost := workloadCPUCost + workloadRAMCost + workloadGPUCost
	totalUsageCost := cpuEff*workloadCPUCost + ramEff*workloadRAMCost + gpuEff*workloadGPUCost
	workloadIdleCost := workloadAllocationCost - totalUsageCost
	infraIdleCost := sa.CPUCostIdle + sa.RAMCostIdle + sa.GPUCostIdle
	totalCost := workloadAllocationCost + infraIdleCost + sa.PVCost + sa.NetworkCost + sa.LoadBalancerCost + sa.SharedCost + sa.ExternalCost
	// PV breakdown: storage is capacity-billed, no usage model
	var pvBreakdown *ResourceCostBreakdown
	if sa.PVCost > 0 {
		pvBreakdown = resourceBreakdown(sa.PVCost, sa.PVCost, 1.0)
	}

	return &ClusterEfficiency{
		Name:                   sa.Name,
		Start:                  sa.Start,
		End:                    sa.End,
		CPUEfficiency:          cpuEff,
		RAMEfficiency:          ramEff,
		GPUEfficiency:          gpuEff,
		TotalUsageCost:         totalUsageCost,
		WorkloadAllocationCost: workloadAllocationCost,
		WorkloadIdleCost:       workloadIdleCost,
		InfraIdleCost:          infraIdleCost,
		TotalIdleCost:          workloadIdleCost + infraIdleCost,
		TotalCost:              totalCost,
		Efficiency:             sa.TotalEfficiency(),
		CPU:                    resourceBreakdown(sa.CPUCost, workloadCPUCost, cpuEff),
		RAM:                    resourceBreakdown(sa.RAMCost, workloadRAMCost, ramEff),
		GPU:                    resourceBreakdown(sa.GPUCost, workloadGPUCost, gpuEff),
		PV:                     pvBreakdown,
	}
}

func (sas *SummaryAllocationSet) ClusterEfficiencySet(groupBy []string) *ClusterEfficiencySet {
	if sas == nil {
		return nil
	}

	workloadByCluster := make(map[string]*SummaryAllocation, len(sas.SummaryAllocations))
	idleByCluster := make(map[string]*SummaryAllocation, len(sas.SummaryAllocations))
	clusters := make(map[string]*ClusterEfficiency, len(sas.SummaryAllocations))
	for _, sa := range sas.SummaryAllocations {
		if sa == nil || sa.IsExternal() || sa.IsUnallocated() || sa.IsUnmounted() {
			continue
		}

		cluster := clusterEfficiencyGroupKey(sa)
		if cluster == "" {
			cluster = sa.Name
		}

		target := workloadByCluster
		if sa.IsIdle() {
			target = idleByCluster
		}

		if existing, ok := target[cluster]; ok {
			_ = existing.Add(sa)
			existing.CPUCostIdle += sa.CPUCostIdle
			existing.GPUCostIdle += sa.GPUCostIdle
			existing.RAMCostIdle += sa.RAMCostIdle
		} else {
			target[cluster] = sa.Clone()
		}
	}

	for cluster, workload := range workloadByCluster {
		idle := idleByCluster[cluster]
		metric := workload.ClusterEfficiencyMetric()
		if idle != nil {
			metric.Name = cluster
			metric.Start = workload.Start
			metric.End = workload.End
			metric.InfraIdleCost += idle.CPUCost + idle.RAMCost + idle.GPUCost
			metric.TotalIdleCost = metric.WorkloadIdleCost + metric.InfraIdleCost
			metric.TotalCost += idle.CPUCost + idle.RAMCost + idle.GPUCost
			metric.Efficiency = kubecostEfficiency(metric.TotalUsageCost, metric.WorkloadAllocationCost+metric.InfraIdleCost)
			if metric.CPU != nil {
				metric.CPU.Allocation += idle.CPUCost
				metric.CPU.Idle += idle.CPUCost
			}
			if metric.RAM != nil {
				metric.RAM.Allocation += idle.RAMCost
				metric.RAM.Idle += idle.RAMCost
			}
			if metric.GPU != nil {
				metric.GPU.Allocation += idle.GPUCost
				metric.GPU.Idle += idle.GPUCost
			}
		}

		clusters[cluster] = metric
	}

	for cluster, idle := range idleByCluster {
		if _, ok := clusters[cluster]; ok {
			continue
		}

		metric := &ClusterEfficiency{
			Name:                   cluster,
			Start:                  idle.Start,
			End:                    idle.End,
			WorkloadAllocationCost: 0.0,
			WorkloadIdleCost:       0.0,
			InfraIdleCost:          idle.CPUCost + idle.RAMCost + idle.GPUCost,
			TotalIdleCost:          idle.CPUCost + idle.RAMCost + idle.GPUCost,
			TotalCost:              idle.CPUCost + idle.RAMCost + idle.GPUCost,
			Efficiency:             0.0,
			CPU:                    resourceBreakdown(idle.CPUCost, 0.0, 0.0),
			RAM:                    resourceBreakdown(idle.RAMCost, 0.0, 0.0),
			GPU:                    resourceBreakdown(idle.GPUCost, 0.0, 0.0),
		}

		clusters[cluster] = metric
	}
	summary := buildSummaryMetric(sas.SummaryAllocations, sas.Window)

	return &ClusterEfficiencySet{
		GroupBy: append([]string(nil), groupBy...),
		Groups:  clusters,
		Summary: summary,
		Window:  sas.Window.Clone(),
	}
}

func clusterEfficiencyGroupKey(sa *SummaryAllocation) string {
	if sa == nil {
		return ""
	}

	if !sa.IsIdle() && sa.Name != "" {
		return sa.Name
	}

	if sa.Properties != nil {
		switch {
		case sa.Properties.Cluster != "":
			return sa.Properties.Cluster
		case sa.Properties.Node != "":
			return sa.Properties.Node
		case sa.Properties.Namespace != "":
			return sa.Properties.Namespace
		case sa.Properties.Pod != "":
			return sa.Properties.Pod
		case sa.Properties.Container != "":
			return sa.Properties.Container
		}
	}

	return sa.Name
}

func (sasr *SummaryAllocationSetRange) ClusterEfficiencySetRange(groupBy []string) *ClusterEfficiencySetRange {
	if sasr == nil {
		return nil
	}

	sets := make([]*ClusterEfficiencySet, 0, len(sasr.SummaryAllocationSets))
	for _, sas := range sasr.SummaryAllocationSets {
		sets = append(sets, sas.ClusterEfficiencySet(groupBy))
	}

	return &ClusterEfficiencySetRange{
		Step:   sasr.Step,
		Sets:   sets,
		Window: sasr.Window.Clone(),
	}
}
