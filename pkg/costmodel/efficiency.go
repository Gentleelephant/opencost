package costmodel

import (
	"fmt"
	"sync"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
)

// Efficiency calculation constants
const (
	DefaultEfficiencyBufferMultiplier = 1.2        // 20% headroom for stability
	EfficiencyMinCPU           = 0.001      // minimum CPU cores
	EfficiencyMinRAM           = 1024 * 1024 // 1 MB minimum RAM
)

// EfficiencyMetric represents efficiency data for a single pod/workload.
type EfficiencyMetric struct {
	Name string `json:"name"` // Pod/namespace/controller name based on aggregation

	// Current state
	CPUEfficiency    float64 `json:"cpuEfficiency"`    // Usage / Request ratio (0-1+)
	MemoryEfficiency float64 `json:"memoryEfficiency"` // Usage / Request ratio (0-1+)

	// Current requests and usage
	CPUCoresRequested float64 `json:"cpuCoresRequested"`
	CPUCoresUsed      float64 `json:"cpuCoresUsed"`
	RAMBytesRequested float64 `json:"ramBytesRequested"`
	RAMBytesUsed      float64 `json:"ramBytesUsed"`

	// Recommendations (based on actual usage with buffer)
	RecommendedCPURequest float64 `json:"recommendedCpuRequest"` // Recommended CPU cores
	RecommendedRAMRequest float64 `json:"recommendedRamRequest"` // Recommended RAM bytes

	// Resulting efficiency after applying recommendations
	ResultingCPUEfficiency    float64 `json:"resultingCpuEfficiency"`
	ResultingMemoryEfficiency float64 `json:"resultingMemoryEfficiency"`

	// Cost analysis
	CurrentTotalCost    float64 `json:"currentTotalCost"`    // Current total cost
	RecommendedCost     float64 `json:"recommendedCost"`     // Estimated cost with recommendations
	CostSavings         float64 `json:"costSavings"`         // Potential cost savings
	CostSavingsPercent  float64 `json:"costSavingsPercent"`  // Savings as percentage

	// Buffer multiplier used for recommendations
	EfficiencyBufferMultiplier float64 `json:"efficiencyBufferMultiplier"` // Buffer multiplier applied (e.g., 1.2 for 20% headroom)

	// Time window
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// safeDiv performs division and returns 0 if denominator is 0.
func safeDiv(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}

// ComputeEfficiencyMetric calculates efficiency metrics for a single allocation.
func ComputeEfficiencyMetric(alloc *opencost.Allocation, bufferMultiplier float64) *EfficiencyMetric {
	if alloc == nil {
		return nil
	}

	// Calculate time duration in hours
	hours := alloc.Minutes() / 60.0
	if hours <= 0 {
		return nil
	}

	// Get current usage (average over the period)
	cpuCoresUsed := alloc.CPUCoreHours / hours
	ramBytesUsed := alloc.RAMByteHours / hours

	// Get requested amounts
	cpuCoresRequested := alloc.CPUCoreRequestAverage
	ramBytesRequested := alloc.RAMBytesRequestAverage

	// Calculate current efficiency (will be 0 if no requests are set)
	cpuEfficiency := safeDiv(cpuCoresUsed, cpuCoresRequested)
	memoryEfficiency := safeDiv(ramBytesUsed, ramBytesRequested)

	// Calculate recommendations with buffer for headroom
	recommendedCPU := cpuCoresUsed * bufferMultiplier
	recommendedRAM := ramBytesUsed * bufferMultiplier

	// Ensure recommendations meet minimum thresholds
	if recommendedCPU < EfficiencyMinCPU {
		recommendedCPU = EfficiencyMinCPU
	}
	if recommendedRAM < EfficiencyMinRAM {
		recommendedRAM = EfficiencyMinRAM
	}

	// Calculate resulting efficiency after applying recommendations
	resultingCPUEff := safeDiv(cpuCoresUsed, recommendedCPU)
	resultingMemEff := safeDiv(ramBytesUsed, recommendedRAM)

	// Calculate cost per unit based on REQUESTED amounts (not used amounts)
	// This gives us the cost per core-hour or byte-hour that the cluster charges
	cpuCostPerCoreHour := safeDiv(alloc.CPUCost, cpuCoresRequested*hours)
	ramCostPerByteHour := safeDiv(alloc.RAMCost, ramBytesRequested*hours)

	// Current total cost
	currentTotalCost := alloc.TotalCost()

	// Estimate recommended cost based on recommended requests
	recommendedCPUCost := recommendedCPU * hours * cpuCostPerCoreHour
	recommendedRAMCost := recommendedRAM * hours * ramCostPerByteHour
	// Keep other costs the same (PV, network, shared, external, GPU)
	otherCosts := alloc.PVCost() + alloc.NetworkCost + alloc.SharedCost + alloc.ExternalCost + alloc.GPUCost
	recommendedTotalCost := recommendedCPUCost + recommendedRAMCost + otherCosts

	// Clamp recommended cost to avoid rounding issues making it higher than current
	if recommendedTotalCost > currentTotalCost && (recommendedTotalCost-currentTotalCost) < 0.0001 {
		recommendedTotalCost = currentTotalCost
	}

	// Calculate savings
	costSavings := currentTotalCost - recommendedTotalCost
	costSavingsPercent := safeDiv(costSavings, currentTotalCost) * 100

	return &EfficiencyMetric{
		Name:                       alloc.Name,
		CPUEfficiency:              cpuEfficiency,
		MemoryEfficiency:           memoryEfficiency,
		CPUCoresRequested:          cpuCoresRequested,
		CPUCoresUsed:               cpuCoresUsed,
		RAMBytesRequested:          ramBytesRequested,
		RAMBytesUsed:               ramBytesUsed,
		RecommendedCPURequest:      recommendedCPU,
		RecommendedRAMRequest:      recommendedRAM,
		ResultingCPUEfficiency:     resultingCPUEff,
		ResultingMemoryEfficiency:  resultingMemEff,
		CurrentTotalCost:           currentTotalCost,
		RecommendedCost:            recommendedTotalCost,
		CostSavings:                costSavings,
		CostSavingsPercent:         costSavingsPercent,
		EfficiencyBufferMultiplier: bufferMultiplier,
		Start:                      alloc.Start,
		End:                        alloc.End,
	}
}

// ComputeEfficiency queries allocations and computes efficiency metrics.
// This is the shared function used by both the MCP server and the HTTP handler.
func ComputeEfficiency(
	model *CostModel,
	window opencost.Window,
	aggregateBy []string,
	filterString string,
	bufferMultiplier float64,
) ([]*EfficiencyMetric, error) {
	// Use the entire window as step to get aggregated data
	step := window.Duration()
	asr, err := model.QueryAllocation(
		window,
		step,
		aggregateBy,
		false, // includeIdle
		false, // idleByNode
		false, // includeProportionalAssetResourceCosts
		false, // includeAggregatedMetadata
		false, // sharedLoadBalancer
		opencost.AccumulateOptionNone,
		false, // shareIdle
		filterString,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query allocations: %w", err)
	}

	// Handle empty results
	if asr == nil || len(asr.Allocations) == 0 {
		return []*EfficiencyMetric{}, nil
	}

	// Compute efficiency metrics from allocations using concurrent processing
	var (
		mu           sync.Mutex
		wg           sync.WaitGroup
		efficiencies = make([]*EfficiencyMetric, 0)
	)

	// Process each allocation set concurrently
	for _, allocSet := range asr.Allocations {
		if allocSet == nil {
			continue
		}

		wg.Add(1)
		go func(allocSet *opencost.AllocationSet) {
			defer wg.Done()

			localMetrics := make([]*EfficiencyMetric, 0, len(allocSet.Allocations))
			for _, alloc := range allocSet.Allocations {
				if metric := ComputeEfficiencyMetric(alloc, bufferMultiplier); metric != nil {
					localMetrics = append(localMetrics, metric)
				}
			}

			if len(localMetrics) > 0 {
				mu.Lock()
				efficiencies = append(efficiencies, localMetrics...)
				mu.Unlock()
			}
		}(allocSet)
	}

	wg.Wait()

	return efficiencies, nil
}
