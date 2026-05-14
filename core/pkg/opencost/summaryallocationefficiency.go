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
	Name               string                   `json:"name"`
	Start              time.Time                `json:"start"`
	End                time.Time                `json:"end"`
	CPUEfficiency      float64                  `json:"cpuEfficiency"`
	RAMEfficiency      float64                  `json:"ramEfficiency"`
	GPUEfficiency      float64                  `json:"gpuEfficiency"`
	TotalUsageCost     float64                  `json:"totalUsageCost"`
	WorkloadIdleCost   float64                  `json:"workloadIdleCost"`
	InfraIdleCost      float64                  `json:"infraIdleCost"`
	TotalIdleCost      float64                  `json:"totalIdleCost"`
	TotalAllocationCost float64                 `json:"totalAllocationCost"`
	Efficiency         float64                  `json:"efficiency"`
	CPU                *ResourceCostBreakdown   `json:"cpu,omitempty"`
	RAM                *ResourceCostBreakdown   `json:"ram,omitempty"`
	GPU                *ResourceCostBreakdown   `json:"gpu,omitempty"`
	PV                 *ResourceCostBreakdown   `json:"pv,omitempty"`
}

type ClusterEfficiencySet struct {
	Clusters map[string]*ClusterEfficiency `json:"clusters"`
	Summary  *ClusterEfficiency            `json:"summary"`
	Window   Window                        `json:"window"`
}

type ClusterEfficiencySetRange struct {
	Step   time.Duration           `json:"step"`
	Sets   []*ClusterEfficiencySet `json:"sets"`
	Window Window                  `json:"window"`
}

func clampEfficiency(value float64) float64 {
	return math.Min(1.0, math.Max(0.0, value))
}

func pageEfficiency(usage, request, cost float64) float64 {
	if request > 0 {
		return clampEfficiency(usage / request)
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

	return pageEfficiency(sa.CPUCoreUsageAverage, sa.CPUCoreRequestAverage, sa.CPUCost)
}

func (sa *SummaryAllocation) ClusterRAMEfficiency() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return pageEfficiency(sa.RAMBytesUsageAverage, sa.RAMBytesRequestAverage, sa.RAMCost)
}

func (sa *SummaryAllocation) ClusterGPUEfficiency() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return pageEfficiency(ptrFloat64Value(sa.GPUUsageAverage), ptrFloat64Value(sa.GPURequestAverage), sa.GPUCost)
}

func (sa *SummaryAllocation) ClusterEfficiencyUsageCost() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return sa.ClusterCPUEfficiency()*(sa.CPUCost-sa.CPUCostIdle) +
		sa.ClusterRAMEfficiency()*(sa.RAMCost-sa.RAMCostIdle) +
		sa.ClusterGPUEfficiency()*(sa.GPUCost-sa.GPUCostIdle)
}

func (sa *SummaryAllocation) ClusterEfficiencyWorkloadIdleCost() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return (1.0-sa.ClusterCPUEfficiency())*(sa.CPUCost-sa.CPUCostIdle) +
		(1.0-sa.ClusterRAMEfficiency())*(sa.RAMCost-sa.RAMCostIdle) +
		(1.0-sa.ClusterGPUEfficiency())*(sa.GPUCost-sa.GPUCostIdle)
}

func (sa *SummaryAllocation) ClusterEfficiencyInfraIdleCost() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return sa.CPUCostIdle + sa.RAMCostIdle + sa.GPUCostIdle
}

func (sa *SummaryAllocation) ClusterEfficiencyTotalIdleCost() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return sa.ClusterEfficiencyWorkloadIdleCost() + sa.ClusterEfficiencyInfraIdleCost()
}

func (sa *SummaryAllocation) ClusterEfficiencyResourceCost() float64 {
	if sa == nil || sa.IsIdle() {
		return 0.0
	}

	return sa.CPUCost + sa.RAMCost + sa.GPUCost
}

func resourceBreakdown(cost, costIdle, efficiency float64) *ResourceCostBreakdown {
	usage := efficiency * (cost - costIdle)
	return &ResourceCostBreakdown{
		Allocation: cost,
		Usage:      usage,
		Idle:       cost - usage,
		Efficiency: efficiency,
	}
}

func (sa *SummaryAllocation) ClusterEfficiencyMetric() *ClusterEfficiency {
	if sa == nil {
		return nil
	}

	cpuEff := sa.ClusterCPUEfficiency()
	ramEff := sa.ClusterRAMEfficiency()
	gpuEff := sa.ClusterGPUEfficiency()

	totalUsageCost := sa.ClusterEfficiencyUsageCost()
	workloadIdleCost := sa.ClusterEfficiencyWorkloadIdleCost()
	infraIdleCost := sa.ClusterEfficiencyInfraIdleCost()
	totalAllocationCost := sa.ClusterEfficiencyResourceCost()
	efficiency := 0.0
	if totalAllocationCost > 0 {
		efficiency = totalUsageCost / totalAllocationCost
	}

	// PV breakdown: storage is capacity-billed, no usage model
	var pvBreakdown *ResourceCostBreakdown
	if sa.PVCost > 0 {
		pvBreakdown = resourceBreakdown(sa.PVCost, 0, 1.0)
	}

	return &ClusterEfficiency{
		Name:                sa.Name,
		Start:               sa.Start,
		End:                 sa.End,
		CPUEfficiency:       cpuEff,
		RAMEfficiency:       ramEff,
		GPUEfficiency:       gpuEff,
		TotalUsageCost:      totalUsageCost,
		WorkloadIdleCost:    workloadIdleCost,
		InfraIdleCost:       infraIdleCost,
		TotalIdleCost:       workloadIdleCost + infraIdleCost,
		TotalAllocationCost: totalAllocationCost,
		Efficiency:          efficiency,
		CPU:                 resourceBreakdown(sa.CPUCost, sa.CPUCostIdle, cpuEff),
		RAM:                 resourceBreakdown(sa.RAMCost, sa.RAMCostIdle, ramEff),
		GPU:                 resourceBreakdown(sa.GPUCost, sa.GPUCostIdle, gpuEff),
		PV:                  pvBreakdown,
	}
}

func (sas *SummaryAllocationSet) ClusterEfficiencySet() *ClusterEfficiencySet {
	if sas == nil {
		return nil
	}

	clusters := make(map[string]*ClusterEfficiency, len(sas.SummaryAllocations))
	totalUsageCost := 0.0
	totalWorkloadIdleCost := 0.0
	totalInfraIdleCost := 0.0
	totalAllocationCost := 0.0

	// Per-resource aggregation
	cpuAlloc, cpuUsage, cpuIdle := 0.0, 0.0, 0.0
	ramAlloc, ramUsage, ramIdle := 0.0, 0.0, 0.0
	gpuAlloc, gpuUsage, gpuIdle := 0.0, 0.0, 0.0
	pvAlloc, pvUsage, pvIdle := 0.0, 0.0, 0.0

	for name, sa := range sas.SummaryAllocations {
		if sa == nil || sa.IsIdle() || sa.IsExternal() || sa.IsUnallocated() || sa.IsUnmounted() {
			continue
		}

		metric := sa.ClusterEfficiencyMetric()
		clusters[name] = metric
		totalUsageCost += metric.TotalUsageCost
		totalWorkloadIdleCost += metric.WorkloadIdleCost
		totalInfraIdleCost += metric.InfraIdleCost
		totalAllocationCost += metric.TotalAllocationCost

		if metric.CPU != nil {
			cpuAlloc += metric.CPU.Allocation
			cpuUsage += metric.CPU.Usage
			cpuIdle += metric.CPU.Idle
		}
		if metric.RAM != nil {
			ramAlloc += metric.RAM.Allocation
			ramUsage += metric.RAM.Usage
			ramIdle += metric.RAM.Idle
		}
		if metric.GPU != nil {
			gpuAlloc += metric.GPU.Allocation
			gpuUsage += metric.GPU.Usage
			gpuIdle += metric.GPU.Idle
		}
		if metric.PV != nil {
			pvAlloc += metric.PV.Allocation
			pvUsage += metric.PV.Usage
			pvIdle += metric.PV.Idle
		}
	}

	summaryCPU := &ResourceCostBreakdown{Allocation: cpuAlloc, Usage: cpuUsage, Idle: cpuIdle}
	if cpuAlloc > 0 {
		summaryCPU.Efficiency = cpuUsage / cpuAlloc
	}
	summaryRAM := &ResourceCostBreakdown{Allocation: ramAlloc, Usage: ramUsage, Idle: ramIdle}
	if ramAlloc > 0 {
		summaryRAM.Efficiency = ramUsage / ramAlloc
	}
	summaryGPU := &ResourceCostBreakdown{Allocation: gpuAlloc, Usage: gpuUsage, Idle: gpuIdle}
	if gpuAlloc > 0 {
		summaryGPU.Efficiency = gpuUsage / gpuAlloc
	}
	summaryPV := &ResourceCostBreakdown{Allocation: pvAlloc, Usage: pvUsage, Idle: pvIdle}
	if pvAlloc > 0 {
		summaryPV.Efficiency = pvUsage / pvAlloc
	}

	summary := &ClusterEfficiency{
		Name:                "summary",
		TotalUsageCost:      totalUsageCost,
		WorkloadIdleCost:    totalWorkloadIdleCost,
		InfraIdleCost:       totalInfraIdleCost,
		TotalIdleCost:       totalWorkloadIdleCost + totalInfraIdleCost,
		TotalAllocationCost: totalAllocationCost,
		CPU:                 summaryCPU,
		RAM:                 summaryRAM,
		GPU:                 summaryGPU,
		PV:                  summaryPV,
	}
	if totalAllocationCost > 0 {
		summary.Efficiency = totalUsageCost / totalAllocationCost
	}

	if sas.Window.Start() != nil {
		summary.Start = *sas.Window.Start()
	}
	if sas.Window.End() != nil {
		summary.End = *sas.Window.End()
	}

	return &ClusterEfficiencySet{
		Clusters: clusters,
		Summary:  summary,
		Window:   sas.Window.Clone(),
	}
}

func (sasr *SummaryAllocationSetRange) ClusterEfficiencySetRange() *ClusterEfficiencySetRange {
	if sasr == nil {
		return nil
	}

	sets := make([]*ClusterEfficiencySet, 0, len(sasr.SummaryAllocationSets))
	for _, sas := range sasr.SummaryAllocationSets {
		sets = append(sets, sas.ClusterEfficiencySet())
	}

	return &ClusterEfficiencySetRange{
		Step:   sasr.Step,
		Sets:   sets,
		Window: sasr.Window.Clone(),
	}
}
