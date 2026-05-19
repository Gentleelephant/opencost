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
	TotalAllocationCost    float64                `json:"totalAllocationCost"`
	Efficiency             float64                `json:"efficiency"`
	CPU                    *ResourceCostBreakdown `json:"cpu,omitempty"`
	RAM                    *ResourceCostBreakdown `json:"ram,omitempty"`
	GPU                    *ResourceCostBreakdown `json:"gpu,omitempty"`
	PV                     *ResourceCostBreakdown `json:"pv,omitempty"`
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
	totalAllocationCost := workloadAllocationCost + infraIdleCost
	efficiency := 0.0
	if totalAllocationCost > 0 {
		efficiency = workloadAllocationCost / totalAllocationCost
	}

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
		TotalAllocationCost:    totalAllocationCost,
		Efficiency:             efficiency,
		CPU:                    resourceBreakdown(sa.CPUCost, workloadCPUCost, cpuEff),
		RAM:                    resourceBreakdown(sa.RAMCost, workloadRAMCost, ramEff),
		GPU:                    resourceBreakdown(sa.GPUCost, workloadGPUCost, gpuEff),
		PV:                     pvBreakdown,
	}
}

func (sas *SummaryAllocationSet) ClusterEfficiencySet() *ClusterEfficiencySet {
	if sas == nil {
		return nil
	}

	workloadByCluster := make(map[string]*SummaryAllocation, len(sas.SummaryAllocations))
	idleByCluster := make(map[string]*SummaryAllocation, len(sas.SummaryAllocations))
	clusters := make(map[string]*ClusterEfficiency, len(sas.SummaryAllocations))
	totalUsageCost := 0.0
	totalWorkloadAllocationCost := 0.0
	totalWorkloadIdleCost := 0.0
	totalInfraIdleCost := 0.0
	totalAllocationCost := 0.0

	// Per-resource aggregation
	cpuAlloc, cpuUsage, cpuIdle := 0.0, 0.0, 0.0
	ramAlloc, ramUsage, ramIdle := 0.0, 0.0, 0.0
	gpuAlloc, gpuUsage, gpuIdle := 0.0, 0.0, 0.0
	pvAlloc, pvUsage, pvIdle := 0.0, 0.0, 0.0

	for _, sa := range sas.SummaryAllocations {
		if sa == nil || sa.IsExternal() || sa.IsUnallocated() || sa.IsUnmounted() {
			continue
		}

		cluster := sa.Name
		if sa.Properties != nil && sa.Properties.Cluster != "" {
			cluster = sa.Properties.Cluster
		}
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
			metric.TotalAllocationCost += idle.CPUCost + idle.RAMCost + idle.GPUCost
			if metric.TotalAllocationCost > 0 {
				metric.Efficiency = metric.WorkloadAllocationCost / metric.TotalAllocationCost
			}
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
		totalUsageCost += metric.TotalUsageCost
		totalWorkloadAllocationCost += metric.WorkloadAllocationCost
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
			TotalAllocationCost:    idle.CPUCost + idle.RAMCost + idle.GPUCost,
			Efficiency:             0.0,
			CPU:                    resourceBreakdown(idle.CPUCost, 0.0, 0.0),
			RAM:                    resourceBreakdown(idle.RAMCost, 0.0, 0.0),
			GPU:                    resourceBreakdown(idle.GPUCost, 0.0, 0.0),
		}

		clusters[cluster] = metric
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
		Name:                   "summary",
		TotalUsageCost:         totalUsageCost,
		WorkloadAllocationCost: totalWorkloadAllocationCost,
		WorkloadIdleCost:       totalWorkloadIdleCost,
		InfraIdleCost:          totalInfraIdleCost,
		TotalIdleCost:          totalWorkloadIdleCost + totalInfraIdleCost,
		TotalAllocationCost:    totalAllocationCost,
		CPU:                    summaryCPU,
		RAM:                    summaryRAM,
		GPU:                    summaryGPU,
		PV:                     summaryPV,
	}
	if totalAllocationCost > 0 {
		summary.Efficiency = totalWorkloadAllocationCost / totalAllocationCost
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
