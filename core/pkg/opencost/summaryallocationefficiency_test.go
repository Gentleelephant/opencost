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

	t.Run("zero usage yields zero efficiency", func(t *testing.T) {
		sa := &SummaryAllocation{
			Name:                   "cluster-a",
			Start:                  start,
			End:                    end,
			CPUCoreRequestAverage:  2.0,
			CPUCoreUsageAverage:    0.0,
			CPUCost:                8.830006,
			CPUCostIdle:            6.495767413178405,
			RAMBytesRequestAverage: 1415334686.6409266,
			RAMBytesUsageAverage:   0.0,
			RAMCost:                4.637674110801063,
			RAMCostIdle:            4.342702102988111,
		}

		metric := sa.ClusterEfficiencyMetric()
		if metric.Efficiency != 0.0 {
			t.Fatalf("expected zero efficiency, got %f", metric.Efficiency)
		}
		if metric.UsageCost != 0.0 {
			t.Fatalf("expected zero usage cost, got %f", metric.UsageCost)
		}
		if metric.WorkloadIdleCost <= 0.0 {
			t.Fatalf("expected positive workload idle cost, got %f", metric.WorkloadIdleCost)
		}
		if metric.InfraIdleCost <= 0.0 {
			t.Fatalf("expected positive infra idle cost, got %f", metric.InfraIdleCost)
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
		Name:                   "17-4",
		Properties:             &AllocationProperties{Cluster: "17-4"},
		Start:                  start,
		End:                    end,
		CPUCoreRequestAverage:  1.7077876447876446,
		CPUCoreUsageAverage:    0,
		CPUCost:                8.830006,
		CPUCostIdle:            6.495767413178405,
		GPUCost:                0,
		GPUCostIdle:            0,
		RAMBytesRequestAverage: 1415334686.6409266,
		RAMBytesUsageAverage:   0,
		RAMCost:                4.637674110801063,
		RAMCostIdle:            4.342702102988111,
	}
	clusterB := &SummaryAllocation{
		Name:                   "host",
		Properties:             &AllocationProperties{Cluster: "host"},
		Start:                  start,
		End:                    end,
		CPUCoreRequestAverage:  8.26166684782608,
		CPUCoreUsageAverage:    1.1137765060294857,
		CPUCost:                22.138237000000025,
		CPUCostIdle:            10.12086959154716,
		GPUCost:                0,
		GPUCostIdle:            0,
		RAMBytesRequestAverage: 13552446556.011595,
		RAMBytesUsageAverage:   16526074005.725855,
		RAMCost:                9.934497107522029,
		RAMCostIdle:            7.470667742561745,
	}
	idle := &SummaryAllocation{
		Name:       "host/__idle__",
		Properties: &AllocationProperties{Cluster: "host"},
		Start:      start,
		End:        end,
		CPUCost:    5.0,
		RAMCost:    3.0,
	}

	sas := &SummaryAllocationSet{
		SummaryAllocations: map[string]*SummaryAllocation{
			clusterA.Name: clusterA,
			clusterB.Name: clusterB,
			idle.Name:     idle,
		},
		Window: window,
	}

	ces := sas.ClusterEfficiencySet()

	if len(ces.Clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(ces.Clusters))
	}

	host := ces.Clusters["host"]
	if host == nil {
		t.Fatalf("expected host cluster in response")
	}

	if !util.IsApproximately(host.CPUEfficiency, 0.1348125658592198) {
		t.Fatalf("unexpected host cpu efficiency: %f", host.CPUEfficiency)
	}
	if !util.IsApproximately(host.RAMEfficiency, 1.0) {
		t.Fatalf("unexpected host ram efficiency: %f", host.RAMEfficiency)
	}
	if !approximatelyWithin(host.UsageCost, 4.084035616675961, 0.001) {
		t.Fatalf("unexpected host usage cost: %f", host.UsageCost)
	}
	if !approximatelyWithin(host.WorkloadIdleCost, 10.39727527324637, 0.001) {
		t.Fatalf("unexpected host workload idle cost: %f", host.WorkloadIdleCost)
	}
	if !approximatelyWithin(host.InfraIdleCost, 17.591537334108904, 0.001) {
		t.Fatalf("unexpected host infra idle cost: %f", host.InfraIdleCost)
	}
	if !approximatelyWithin(host.TotalIdleCost, 27.988812607355278, 0.001) {
		t.Fatalf("unexpected host total idle cost: %f", host.TotalIdleCost)
	}
	if !util.IsApproximately(host.ResourceCost, 32.07273410752205) {
		t.Fatalf("unexpected host resource cost: %f", host.ResourceCost)
	}
	if !approximatelyWithin(host.Efficiency, 0.1273367391788061, 0.0001) {
		t.Fatalf("unexpected host efficiency: %f", host.Efficiency)
	}

	if !approximatelyWithin(ces.Summary.UsageCost, 4.084035616675961, 0.001) {
		t.Fatalf("unexpected total usage cost: %f", ces.Summary.UsageCost)
	}
	if !approximatelyWithin(ces.Summary.WorkloadIdleCost, 13.026485867880915, 0.001) {
		t.Fatalf("unexpected total workload idle cost: %f", ces.Summary.WorkloadIdleCost)
	}
	if !approximatelyWithin(ces.Summary.InfraIdleCost, 28.430006850275422, 0.001) {
		t.Fatalf("unexpected total infra idle cost: %f", ces.Summary.InfraIdleCost)
	}
	if !approximatelyWithin(ces.Summary.TotalIdleCost, 41.45649271815634, 0.001) {
		t.Fatalf("unexpected total idle cost: %f", ces.Summary.TotalIdleCost)
	}
	if !util.IsApproximately(ces.Summary.ResourceCost, 45.54041421832311) {
		t.Fatalf("unexpected total resource cost: %f", ces.Summary.ResourceCost)
	}
	if !approximatelyWithin(ces.Summary.Efficiency, 0.08967936515945769, 0.0001) {
		t.Fatalf("unexpected total efficiency: %f", ces.Summary.Efficiency)
	}

	avgOfClusters := (ces.Clusters["17-4"].Efficiency + ces.Clusters["host"].Efficiency) / 2.0
	if util.IsApproximately(avgOfClusters, ces.Summary.Efficiency) {
		t.Fatalf("expected summary efficiency to be ratio-of-sums, not simple average")
	}
}
