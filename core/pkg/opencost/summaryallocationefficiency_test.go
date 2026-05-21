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

	t.Run("top level efficiency matches allocation total efficiency", func(t *testing.T) {
		sa := &SummaryAllocation{
			Name:                  "cluster-a",
			Start:                 start,
			End:                   end,
			CPUCoreRequestAverage: 2.0,
			CPUCoreUsageAverage:   1.0,
			CPUCost:               100.0,
		}

		metric := sa.ClusterEfficiencyMetric()
		if !approximatelyWithin(metric.Efficiency, 0.5, 0.0001) {
			t.Fatalf("expected efficiency 0.5 from allocation formula, got %f", metric.Efficiency)
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

	t.Run("usage greater than request is not clamped", func(t *testing.T) {
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

		if sa.ClusterCPUEfficiency() != 4.0 {
			t.Fatalf("expected cpu efficiency 4.0, got %f", sa.ClusterCPUEfficiency())
		}
		if sa.ClusterRAMEfficiency() != 5.0 {
			t.Fatalf("expected ram efficiency 5.0, got %f", sa.ClusterRAMEfficiency())
		}
	})

	t.Run("pv cost contributes to total cost but not idle or efficiency", func(t *testing.T) {
		sa := &SummaryAllocation{
			Name:                  "cluster-d",
			Start:                 start,
			End:                   end,
			CPUCoreRequestAverage: 2.0,
			CPUCoreUsageAverage:   1.0,
			CPUCost:               100.0,
			PVCost:                25.0,
		}

		metric := sa.ClusterEfficiencyMetric()
		if !approximatelyWithin(metric.TotalCost, 125.0, 0.0001) {
			t.Fatalf("expected total cost 125, got %f", metric.TotalCost)
		}
		if !approximatelyWithin(metric.TotalIdleCost, 50.0, 0.0001) {
			t.Fatalf("expected total idle 50, got %f", metric.TotalIdleCost)
		}
		if !approximatelyWithin(metric.Efficiency, 0.5, 0.0001) {
			t.Fatalf("expected efficiency 0.5, got %f", metric.Efficiency)
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

	ces := sas.ClusterEfficiencySet([]string{AllocationClusterProp})

	if len(ces.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(ces.Groups))
	}
	if len(ces.GroupBy) != 1 || ces.GroupBy[0] != AllocationClusterProp {
		t.Fatalf("unexpected groupBy: %#v", ces.GroupBy)
	}

	clusterAMetric := ces.Groups["cluster-a"]
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
	if !util.IsApproximately(clusterAMetric.TotalCost, 120.0) {
		t.Fatalf("unexpected cluster-a total cost: %f", clusterAMetric.TotalCost)
	}
	if !approximatelyWithin(clusterAMetric.Efficiency, 50.0/120.0, 0.0001) {
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
	if !util.IsApproximately(ces.Summary.TotalCost, 220.0) {
		t.Fatalf("unexpected total cost: %f", ces.Summary.TotalCost)
	}
	if !approximatelyWithin(ces.Summary.Efficiency, 60.0/110.0, 0.0001) {
		t.Fatalf("unexpected total efficiency: %f", ces.Summary.Efficiency)
	}

}

func TestSummaryAllocationSetClusterEfficiencySet_NodeGroupingUsesAllocationName(t *testing.T) {
	start := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	window := NewWindow(&start, &end)

	nodeA := &SummaryAllocation{
		Name:                  "node-a",
		Properties:            &AllocationProperties{Cluster: "cluster-a", Node: "node-a"},
		Start:                 start,
		End:                   end,
		CPUCoreRequestAverage: 4.0,
		CPUCoreUsageAverage:   2.0,
		CPUCost:               40.0,
	}
	nodeB := &SummaryAllocation{
		Name:                  "node-b",
		Properties:            &AllocationProperties{Cluster: "cluster-a", Node: "node-b"},
		Start:                 start,
		End:                   end,
		CPUCoreRequestAverage: 2.0,
		CPUCoreUsageAverage:   1.0,
		CPUCost:               20.0,
	}

	sas := &SummaryAllocationSet{
		SummaryAllocations: map[string]*SummaryAllocation{
			nodeA.Name: nodeA,
			nodeB.Name: nodeB,
		},
		Window: window,
	}

	ces := sas.ClusterEfficiencySet([]string{AllocationNodeProp})
	if len(ces.Groups) != 2 {
		t.Fatalf("expected 2 grouped results, got %d", len(ces.Groups))
	}
	if ces.Groups["node-a"] == nil {
		t.Fatalf("expected node-a in grouped results")
	}
	if ces.Groups["node-b"] == nil {
		t.Fatalf("expected node-b in grouped results")
	}
	if len(ces.GroupBy) != 1 || ces.GroupBy[0] != AllocationNodeProp {
		t.Fatalf("unexpected groupBy: %#v", ces.GroupBy)
	}
}

func TestSummaryAllocationSetClusterEfficiencySet_SummaryEfficiencyIsRecomputedFromInputs(t *testing.T) {
	start := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	window := NewWindow(&start, &end)

	groupA := &SummaryAllocation{
		Name:                  "group-a",
		Properties:            &AllocationProperties{Cluster: "cluster-a", Node: "group-a"},
		Start:                 start,
		End:                   end,
		CPUCoreRequestAverage: 10.0,
		CPUCoreUsageAverage:   5.0,
		CPUCost:               100.0,
	}
	groupB := &SummaryAllocation{
		Name:                  "group-b",
		Properties:            &AllocationProperties{Cluster: "cluster-a", Node: "group-b"},
		Start:                 start,
		End:                   end,
		CPUCoreRequestAverage: 1.0,
		CPUCoreUsageAverage:   1.0,
		CPUCost:               10.0,
	}
	idle := &SummaryAllocation{
		Name:       "cluster-a/__idle__",
		Properties: &AllocationProperties{Cluster: "cluster-a"},
		Start:      start,
		End:        end,
		CPUCost:    90.0,
	}

	sas := &SummaryAllocationSet{
		SummaryAllocations: map[string]*SummaryAllocation{
			groupA.Name: groupA,
			groupB.Name: groupB,
			idle.Name:   idle,
		},
		Window: window,
	}

	ces := sas.ClusterEfficiencySet([]string{AllocationClusterProp, AllocationNodeProp})
	if ces.Summary == nil {
		t.Fatalf("expected summary in response")
	}

	expected := 60.0 / 110.0
	if !approximatelyWithin(ces.Summary.Efficiency, expected, 0.0001) {
		t.Fatalf("expected recomputed summary efficiency %f, got %f", expected, ces.Summary.Efficiency)
	}

	averageOfGroups := (ces.Groups["group-a"].Efficiency + ces.Groups["group-b"].Efficiency) / 2.0
	if approximatelyWithin(ces.Summary.Efficiency, averageOfGroups, 0.0001) {
		t.Fatalf("summary efficiency should not equal simple average of group efficiencies; both were %f", averageOfGroups)
	}
}
