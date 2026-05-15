package costmodel

import (
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util"
)

func TestScaleHourlyCostData(t *testing.T) {
	costData := map[string]*CostData{}

	start := 1570000000
	oneHour := 60 * 60

	generateVectorSeries := func(start, count, interval int, value float64) []*util.Vector {
		vs := []*util.Vector{}
		for i := 0; i < count; i++ {
			v := &util.Vector{
				Timestamp: float64(start + (i * oneHour)),
				Value:     value,
			}
			vs = append(vs, v)
		}
		return vs
	}

	costData["default"] = &CostData{
		RAMReq:        generateVectorSeries(start, 100, oneHour, 1.0),
		RAMUsed:       generateVectorSeries(start, 0, oneHour, 1.0),
		RAMAllocation: generateVectorSeries(start, 100, oneHour, 107.226),
		CPUReq:        generateVectorSeries(start, 100, oneHour, 0.00317),
		CPUUsed:       generateVectorSeries(start, 95, oneHour, 1.0),
		CPUAllocation: generateVectorSeries(start, 2, oneHour, 123.456),
		PVCData: []*PersistentVolumeClaimData{
			{Values: generateVectorSeries(start, 100, oneHour, 1.34)},
		},
	}

	compressedData := ScaleHourlyCostData(costData, 10)

	act, ok := compressedData["default"]
	if !ok {
		t.Errorf("compressed data should have key \"default\"")
	}

	// RAMReq
	if len(act.RAMReq) != 10 {
		t.Errorf("expected RAMReq to have length %d, was actually %d", 10, len(act.RAMReq))
	}
	for _, val := range act.RAMReq {
		if val.Value != 10.0 {
			t.Errorf("expected each RAMReq Vector to have Value %f, was actually %f", 10.0, val.Value)
		}
	}

	// RAMUsed
	if len(act.RAMUsed) != 0 {
		t.Errorf("expected RAMUsed to have length %d, was actually %d", 0, len(act.RAMUsed))
	}

	// RAMAllocation
	if len(act.RAMAllocation) != 10 {
		t.Errorf("expected RAMAllocation to have length %d, was actually %d", 10, len(act.RAMAllocation))
	}
	for _, val := range act.RAMAllocation {
		if val.Value != 1072.26 {
			t.Errorf("expected each RAMAllocation Vector to have Value %f, was actually %f", 1072.26, val.Value)
		}
	}

	// CPUReq
	if len(act.CPUReq) != 10 {
		t.Errorf("expected CPUReq to have length %d, was actually %d", 10, len(act.CPUReq))
	}
	for _, val := range act.CPUReq {
		if val.Value != 0.0317 {
			t.Errorf("expected each CPUReq Vector to have Value %f, was actually %f", 0.0317, val.Value)
		}
	}

	// CPUUsed
	if len(act.CPUUsed) != 10 {
		t.Errorf("expected CPUUsed to have length %d, was actually %d", 10, len(act.CPUUsed))
	}
	for _, val := range act.CPUUsed[:len(act.CPUUsed)-1] {
		if val.Value != 10.0 {
			t.Errorf("expected each CPUUsed Vector to have Value %f, was actually %f", 10.0, val.Value)
		}
	}
	if act.CPUUsed[len(act.CPUUsed)-1].Value != 5.0 {
		t.Errorf("expected each CPUUsed Vector to have Value %f, was actually %f", 5.0, act.CPUUsed[len(act.CPUUsed)-1].Value)
	}

	// CPUAllocation
	if len(act.CPUAllocation) != 1 {
		t.Errorf("expected CPUAllocation to have length %d, was actually %d", 1, len(act.CPUAllocation))
	}
	if act.CPUAllocation[0].Value != 246.912 {
		t.Errorf("expected each CPUAllocation Vector to have Value %f, was actually %f", 246.912, act.CPUAllocation[len(act.CPUAllocation)-1].Value)
	}

	// PVCData
	if len(act.PVCData[0].Values) != 10 {
		t.Errorf("expected PVCData[0] to have length %d, was actually %d", 10, len(act.PVCData[0].Values))
	}
	for _, val := range act.PVCData[0].Values {
		if val.Value != 13.4 {
			t.Errorf("expected each PVCData[0] Vector to have Value %f, was actually %f", 13.4, val.Value)
		}
	}

	costData["default"] = &CostData{
		RAMReq:        generateVectorSeries(start, 100, oneHour, 1.0),
		RAMUsed:       generateVectorSeries(start, 0, oneHour, 1.0),
		RAMAllocation: generateVectorSeries(start, 100, oneHour, 107.226),
		CPUReq:        generateVectorSeries(start, 100, oneHour, 0.00317),
		CPUUsed:       generateVectorSeries(start, 95, oneHour, 1.0),
		CPUAllocation: generateVectorSeries(start, 2, oneHour, 124.6),
		PVCData: []*PersistentVolumeClaimData{
			{Values: generateVectorSeries(start, 100, oneHour, 1.34)},
		},
	}

	scaledData := ScaleHourlyCostData(costData, 0.1)

	act, ok = scaledData["default"]
	if !ok {
		t.Errorf("scaled data should have key \"default\"")
	}

	// RAMReq
	if len(act.RAMReq) != 100 {
		t.Errorf("expected RAMReq to have length %d, was actually %d", 100, len(act.RAMReq))
	}
	for _, val := range act.RAMReq {
		if val.Value != 0.1 {
			t.Errorf("expected each RAMReq Vector to have Value %f, was actually %f", 0.1, val.Value)
		}
	}

	// RAMUsed
	if len(act.RAMUsed) != 0 {
		t.Errorf("expected RAMUsed to have length %d, was actually %d", 0, len(act.RAMUsed))
	}

	// RAMAllocation
	if len(act.RAMAllocation) != 100 {
		t.Errorf("expected RAMAllocation to have length %d, was actually %d", 100, len(act.RAMAllocation))
	}
	for _, val := range act.RAMAllocation {
		if val.Value != 10.7226 {
			t.Errorf("expected each RAMAllocation Vector to have Value %f, was actually %f", 10.7226, val.Value)
		}
	}

	// CPUReq
	if len(act.CPUReq) != 100 {
		t.Errorf("expected CPUReq to have length %d, was actually %d", 100, len(act.CPUReq))
	}
	for _, val := range act.CPUReq {
		if val.Value != 0.000317 {
			t.Errorf("expected each CPUReq Vector to have Value %f, was actually %f", 0.000317, val.Value)
		}
	}

	// CPUUsed
	if len(act.CPUUsed) != 95 {
		t.Errorf("expected CPUUsed to have length %d, was actually %d", 95, len(act.CPUUsed))
	}
	for _, val := range act.CPUUsed {
		if val.Value != 0.1 {
			t.Errorf("expected each CPUUsed Vector to have Value %f, was actually %f", 0.1, val.Value)
		}
	}

	// CPUAllocation
	if len(act.CPUAllocation) != 2 {
		t.Errorf("expected CPUAllocation to have length %d, was actually %d", 2, len(act.CPUAllocation))
	}
	for _, val := range act.CPUAllocation {
		if val.Value != 12.46 {
			t.Errorf("expected each CPUAllocation Vector to have Value %f, was actually %f", 12.46, val.Value)
		}
	}

	// PVCData
	if len(act.PVCData[0].Values) != 100 {
		t.Errorf("expected PVCData[0] to have length %d, was actually %d", 100, len(act.PVCData[0].Values))
	}
	for _, val := range act.PVCData[0].Values {
		if val.Value != .134 {
			t.Errorf("expected each PVCData[0] Vector to have Value %f, was actually %f", .134, val.Value)
		}
	}
}

func TestParseAggregationProperties_Default(t *testing.T) {
	got, err := ParseAggregationProperties([]string{})
	expected := []string{
		opencost.AllocationClusterProp,
		opencost.AllocationNodeProp,
		opencost.AllocationNamespaceProp,
		opencost.AllocationPodProp,
		opencost.AllocationContainerProp,
	}

	if err != nil {
		t.Fatalf("TestParseAggregationPropertiesDefault: unexpected error: %s", err)
	}

	if len(expected) != len(got) {
		t.Fatalf("TestParseAggregationPropertiesDefault: expected length of %d, got: %d", len(expected), len(got))
	}

	for i := range got {
		if got[i] != expected[i] {
			t.Fatalf("TestParseAggregationPropertiesDefault: expected[i] should be %s, got[i]:%s", expected[i], got[i])
		}
	}
}

func TestNormalizeAllocationFilterString_ImplicitAnd(t *testing.T) {
	input := `cluster:"host"   namespace:"default"`
	expected := `cluster:"host" + namespace:"default"`

	got := normalizeAllocationFilterString(input)
	if got != expected {
		t.Fatalf("expected normalized filter %q, got %q", expected, got)
	}
}

func TestBuildAllocationFilter_ImplicitAndMatchesBothClauses(t *testing.T) {
	filter, err := buildAllocationFilter(`cluster:"host" namespace:"default"`)
	if err != nil {
		t.Fatalf("unexpected error building filter: %v", err)
	}

	matching := &opencost.Allocation{
		Properties: &opencost.AllocationProperties{
			Cluster:   "host",
			Namespace: "default",
		},
	}
	if !filter.Matches(matching) {
		t.Fatalf("expected filter to match allocation with cluster=host and namespace=default")
	}

	nonMatching := &opencost.Allocation{
		Properties: &opencost.AllocationProperties{
			Cluster:   "host",
			Namespace: "kube-system",
		},
	}
	if filter.Matches(nonMatching) {
		t.Fatalf("expected filter not to match allocation with namespace=kube-system")
	}
}

func TestBuildSummaryAllocationToplineResponse(t *testing.T) {
	start := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	mid := start.Add(24 * time.Hour)
	end := start.Add(48 * time.Hour)

	sas := &opencost.SummaryAllocationSet{
		SummaryAllocations: map[string]*opencost.SummaryAllocation{
			"ns-a": {
				Name:                   "ns-a",
				Start:                  start,
				End:                    mid,
				CPUCoreRequestAverage:  2,
				CPUCoreUsageAverage:    1,
				CPUCost:                10,
				CPUCostIdle:            3,
				GPUCostIdle:            1,
				PVCost:                 1.5,
				RAMBytesRequestAverage: 100,
				RAMBytesUsageAverage:   80,
				RAMCost:                4,
				RAMCostIdle:            1,
				Efficiency:             0,
			},
			"ns-b": {
				Name:                   "ns-b",
				Start:                  mid,
				End:                    end,
				CPUCoreRequestAverage:  4,
				CPUCoreUsageAverage:    2,
				CPUCost:                20,
				CPUCostIdle:            5,
				GPUCostIdle:            2,
				PVCost:                 2.5,
				RAMBytesRequestAverage: 200,
				RAMBytesUsageAverage:   120,
				RAMCost:                6,
				RAMCostIdle:            2,
				Efficiency:             0,
			},
		},
		Window: opencost.NewClosedWindow(start, end),
	}

	resp, err := buildSummaryAllocationToplineResponse(opencost.NewSummaryAllocationSetRange(sas))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.NumResults != 2 {
		t.Fatalf("expected numResults=2, got %d", resp.NumResults)
	}

	total := resp.Combined.Allocations["total"]
	if total == nil {
		t.Fatalf("expected combined.allocations.total to be present")
	}

	if total.Name != "total" {
		t.Fatalf("expected total name to be total, got %q", total.Name)
	}

	if total.CPUCost != 30 {
		t.Fatalf("expected total CPU cost 30, got %f", total.CPUCost)
	}

	if total.PVCost != 4 {
		t.Fatalf("expected total PV cost 4, got %f", total.PVCost)
	}

	if total.RAMCost != 10 {
		t.Fatalf("expected total RAM cost 10, got %f", total.RAMCost)
	}

	if total.CPUCostIdle != 8 {
		t.Fatalf("expected total CPU idle cost 8, got %f", total.CPUCostIdle)
	}

	if total.GPUCostIdle != 3 {
		t.Fatalf("expected total GPU idle cost 3, got %f", total.GPUCostIdle)
	}

	if total.RAMCostIdle != 3 {
		t.Fatalf("expected total RAM idle cost 3, got %f", total.RAMCostIdle)
	}

	if total.Start != start {
		t.Fatalf("expected total start %s, got %s", start, total.Start)
	}

	if total.End != end {
		t.Fatalf("expected total end %s, got %s", end, total.End)
	}
}

func TestSummaryAllocationToToplineItem_PreservesExplicitZeroEfficiency(t *testing.T) {
	item := summaryAllocationToToplineItem(&opencost.SummaryAllocation{
		Name:                  "ns-a",
		Start:                 time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
		End:                   time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		CPUCoreRequestAverage: 4,
		CPUCoreUsageAverage:   0,
		CPUCost:               10,
		RAMCost:               5,
		Efficiency:            0,
	})

	if item == nil {
		t.Fatalf("expected non-nil item")
	}

	if item.Efficiency != 0 {
		t.Fatalf("expected explicit zero efficiency to be preserved, got %f", item.Efficiency)
	}
}

func TestParseAggregationProperties_All(t *testing.T) {
	got, err := ParseAggregationProperties([]string{"all"})

	if err != nil {
		t.Fatalf("TestParseAggregationPropertiesDefault: unexpected error: %s", err)
	}

	if len(got) != 0 {
		t.Fatalf("TestParseAggregationPropertiesDefault: expected length of 0, got: %d", len(got))
	}
}
