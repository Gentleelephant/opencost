package opencost

import (
	"math"
	"time"
)

type ClusterEfficiency struct {
	Name             string    `json:"name"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
	CPUEfficiency    float64   `json:"cpuEfficiency"`
	RAMEfficiency    float64   `json:"ramEfficiency"`
	GPUEfficiency    float64   `json:"gpuEfficiency"`
	UsageCost        float64   `json:"usageCost"`
	WorkloadIdleCost float64   `json:"workloadIdleCost"`
	InfraIdleCost    float64   `json:"infraIdleCost"`
	TotalIdleCost    float64   `json:"totalIdleCost"`
	ResourceCost     float64   `json:"resourceCost"`
	Efficiency       float64   `json:"efficiency"`
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

func (sa *SummaryAllocation) ClusterEfficiencyMetric() *ClusterEfficiency {
	if sa == nil {
		return nil
	}

	usageCost := sa.ClusterEfficiencyUsageCost()
	workloadIdleCost := sa.ClusterEfficiencyWorkloadIdleCost()
	infraIdleCost := sa.ClusterEfficiencyInfraIdleCost()
	resourceCost := sa.ClusterEfficiencyResourceCost()
	efficiency := 0.0
	if resourceCost > 0 {
		efficiency = usageCost / resourceCost
	}

	return &ClusterEfficiency{
		Name:             sa.Name,
		Start:            sa.Start,
		End:              sa.End,
		CPUEfficiency:    sa.ClusterCPUEfficiency(),
		RAMEfficiency:    sa.ClusterRAMEfficiency(),
		GPUEfficiency:    sa.ClusterGPUEfficiency(),
		UsageCost:        usageCost,
		WorkloadIdleCost: workloadIdleCost,
		InfraIdleCost:    infraIdleCost,
		TotalIdleCost:    workloadIdleCost + infraIdleCost,
		ResourceCost:     resourceCost,
		Efficiency:       efficiency,
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
	totalResourceCost := 0.0

	for name, sa := range sas.SummaryAllocations {
		if sa == nil || sa.IsIdle() || sa.IsExternal() || sa.IsUnallocated() || sa.IsUnmounted() {
			continue
		}

		metric := sa.ClusterEfficiencyMetric()
		clusters[name] = metric
		totalUsageCost += metric.UsageCost
		totalWorkloadIdleCost += metric.WorkloadIdleCost
		totalInfraIdleCost += metric.InfraIdleCost
		totalResourceCost += metric.ResourceCost
	}

	summary := &ClusterEfficiency{
		Name:             "summary",
		UsageCost:        totalUsageCost,
		WorkloadIdleCost: totalWorkloadIdleCost,
		InfraIdleCost:    totalInfraIdleCost,
		TotalIdleCost:    totalWorkloadIdleCost + totalInfraIdleCost,
		ResourceCost:     totalResourceCost,
	}
	if totalResourceCost > 0 {
		summary.Efficiency = totalUsageCost / totalResourceCost
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
