package opencost

import (
	"math"
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/util"
)

func approximatelyWithin(actual, expected, tolerance float64) bool {
	return math.Abs(actual-expected) <= tolerance
}

func TestSummaryAllocationClusterEfficiencyMetric(t *testing.T) {
	start := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)

	t.Run("top level efficiency uses workload allocation cost", func(t *testing.T) {
		sa := &SummaryAllocation{
			Name:                  "cluster-a",
			Start:                 start,
			End:                   end,
			CPUCoreRequestAverage: 2.0,
			CPUCoreUsageAverage:   1.0,
			CPUCost:               100.0,
		}

		metric := sa.ClusterEfficiencyMetric()
		if !approximatelyWithin(metric.Efficiency, 1.0, 0.0001) {
			t.Fatalf("expected efficiency 1.0 without cluster idle, got %f", metric.Efficiency)
		}
		if !approximatelyWithin(metric.TotalUsageCost, 50.0, 0.0001) {
			t.Fatalf("expected usage cost 50, got %f", metric.TotalUsageCost)
		}
		if !approximatelyWithin(metric.WorkloadAllocationCost, 100.0, 0.0001) {
			t.Fatalf("expected workload allocation cost 100, got %f", metric.WorkloadAllocationCost)
		}
		if !approximatelyWithin(metric.WorkloadIdleCost, 50.0, 0.0001) {
			t.Fatalf("expected workload idle cost 50, got %f", metric.WorkloadIdleCost)
		}
		if metric.InfraIdleCost != 0.0 {
			t.Fatalf("expected zero infra idle cost, got %f", metric.InfraIdleCost)
		}
	})

	t.Run("request-free usage falls back to full efficiency", func(t *testing.T) {
		sa := &SummaryAllocation{
			Name:                "cluster-b",
			Start:               start,
			End:                 end,
			CPUCoreUsageAverage: 0.5,
			CPUCost:             10.0,
		}

		if sa.ClusterCPUEfficiency() != 1.0 {
			t.Fatalf("expected cpu efficiency fallback to 1.0, got %f", sa.ClusterCPUEfficiency())
		}
	})

	t.Run("usage greater than request clamps to one", func(t *testing.T) {
		sa := &SummaryAllocation{
			Name:                   "cluster-c",
			Start:                  start,
			End:                    end,
			CPUCoreRequestAverage:  1.0,
			CPUCoreUsageAverage:    4.0,
			CPUCost:                3.0,
			RAMBytesRequestAverage: 1.0,
			RAMBytesUsageAverage:   5.0,
			RAMCost:                2.0,
		}

		if sa.ClusterCPUEfficiency() != 1.0 {
			t.Fatalf("expected clamped cpu efficiency 1.0, got %f", sa.ClusterCPUEfficiency())
		}
		if sa.ClusterRAMEfficiency() != 1.0 {
			t.Fatalf("expected clamped ram efficiency 1.0, got %f", sa.ClusterRAMEfficiency())
		}
	})
}

func TestSummaryAllocationSetClusterEfficiencySet(t *testing.T) {
	start := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	window := NewWindow(&start, &end)

	clusterA := &SummaryAllocation{
		Name:                  "cluster-a",
		Properties:            &AllocationProperties{Cluster: "cluster-a"},
		Start:                 start,
		End:                   end,
		CPUCoreRequestAverage: 10.0,
		CPUCoreUsageAverage:   5.0,
		CPUCost:               100.0,
	}
	clusterAIdle := &SummaryAllocation{
		Name:       "cluster-a/__idle__",
		Properties: &AllocationProperties{Cluster: "cluster-a"},
		Start:      start,
		End:        end,
		CPUCost:    20.0,
	}
	clusterB := &SummaryAllocation{
		Name:                  "cluster-b",
		Properties:            &AllocationProperties{Cluster: "cluster-b"},
		Start:                 start,
		End:                   end,
		CPUCoreRequestAverage: 10.0,
		CPUCoreUsageAverage:   10.0,
		CPUCost:               10.0,
	}
	clusterBIdle := &SummaryAllocation{
		Name:       "cluster-b/__idle__",
		Properties: &AllocationProperties{Cluster: "cluster-b"},
		Start:      start,
		End:        end,
		CPUCost:    90.0,
	}

	sas := &SummaryAllocationSet{
		SummaryAllocations: map[string]*SummaryAllocation{
			clusterA.Name:     clusterA,
			clusterAIdle.Name: clusterAIdle,
			clusterB.Name:     clusterB,
			clusterBIdle.Name: clusterBIdle,
		},
		Window: window,
	}

	ces := sas.ClusterEfficiencySet()

	if len(ces.Clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(ces.Clusters))
	}

	clusterAMetric := ces.Clusters["cluster-a"]
	if clusterAMetric == nil {
		t.Fatalf("expected cluster-a in response")
	}

	if !util.IsApproximately(clusterAMetric.CPUEfficiency, 0.5) {
		t.Fatalf("unexpected cluster-a cpu efficiency: %f", clusterAMetric.CPUEfficiency)
	}
	if !approximatelyWithin(clusterAMetric.TotalUsageCost, 50.0, 0.001) {
		t.Fatalf("unexpected cluster-a usage cost: %f", clusterAMetric.TotalUsageCost)
	}
	if !approximatelyWithin(clusterAMetric.WorkloadAllocationCost, 100.0, 0.001) {
		t.Fatalf("unexpected cluster-a workload allocation cost: %f", clusterAMetric.WorkloadAllocationCost)
	}
	if !approximatelyWithin(clusterAMetric.WorkloadIdleCost, 50.0, 0.001) {
		t.Fatalf("unexpected cluster-a workload idle cost: %f", clusterAMetric.WorkloadIdleCost)
	}
	if !approximatelyWithin(clusterAMetric.InfraIdleCost, 20.0, 0.001) {
		t.Fatalf("unexpected cluster-a infra idle cost: %f", clusterAMetric.InfraIdleCost)
	}
	if !approximatelyWithin(clusterAMetric.TotalIdleCost, 70.0, 0.001) {
		t.Fatalf("unexpected cluster-a total idle cost: %f", clusterAMetric.TotalIdleCost)
	}
	if !util.IsApproximately(clusterAMetric.TotalAllocationCost, 120.0) {
		t.Fatalf("unexpected cluster-a resource cost: %f", clusterAMetric.TotalAllocationCost)
	}
	if !approximatelyWithin(clusterAMetric.Efficiency, 100.0/120.0, 0.0001) {
		t.Fatalf("unexpected cluster-a efficiency: %f", clusterAMetric.Efficiency)
	}

	if !approximatelyWithin(ces.Summary.TotalUsageCost, 60.0, 0.001) {
		t.Fatalf("unexpected total usage cost: %f", ces.Summary.TotalUsageCost)
	}
	if !approximatelyWithin(ces.Summary.WorkloadAllocationCost, 110.0, 0.001) {
		t.Fatalf("unexpected total workload allocation cost: %f", ces.Summary.WorkloadAllocationCost)
	}
	if !approximatelyWithin(ces.Summary.WorkloadIdleCost, 50.0, 0.001) {
		t.Fatalf("unexpected total workload idle cost: %f", ces.Summary.WorkloadIdleCost)
	}
	if !approximatelyWithin(ces.Summary.InfraIdleCost, 110.0, 0.001) {
		t.Fatalf("unexpected total infra idle cost: %f", ces.Summary.InfraIdleCost)
	}
	if !approximatelyWithin(ces.Summary.TotalIdleCost, 160.0, 0.001) {
		t.Fatalf("unexpected total idle cost: %f", ces.Summary.TotalIdleCost)
	}
	if !util.IsApproximately(ces.Summary.TotalAllocationCost, 220.0) {
		t.Fatalf("unexpected total resource cost: %f", ces.Summary.TotalAllocationCost)
	}
	if !approximatelyWithin(ces.Summary.Efficiency, 0.5, 0.0001) {
		t.Fatalf("unexpected total efficiency: %f", ces.Summary.Efficiency)
	}

	avgOfClusters := (ces.Clusters["cluster-a"].Efficiency + ces.Clusters["cluster-b"].Efficiency) / 2.0
	if util.IsApproximately(avgOfClusters, ces.Summary.Efficiency) {
		t.Fatalf("expected summary efficiency to be ratio-of-sums, not simple average")
	}
}
