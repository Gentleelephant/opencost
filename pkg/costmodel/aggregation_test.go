package costmodel

import (
	"net/url"
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
)

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

func TestResolveAccumulateOption(t *testing.T) {
	tests := []struct {
		name       string
		accumulate opencost.AccumulateOption
		input      string
		expected   opencost.AccumulateOption
		expectErr  bool
	}{
		{
			name:       "accumulate false without accumulateBy",
			accumulate: opencost.AccumulateOptionNone,
			input:      "",
			expected:   opencost.AccumulateOptionNone,
		},
		{
			name:       "accumulate true without accumulateBy defaults to all",
			accumulate: opencost.AccumulateOptionAll,
			input:      "",
			expected:   opencost.AccumulateOptionAll,
		},
		{
			name:       "accumulate day is preserved",
			accumulate: opencost.AccumulateOptionDay,
			input:      "",
			expected:   opencost.AccumulateOptionDay,
		},
		{
			name:       "accumulate week is preserved",
			accumulate: opencost.AccumulateOptionWeek,
			input:      "",
			expected:   opencost.AccumulateOptionWeek,
		},
		{
			name:       "accumulateBy overrides accumulate",
			accumulate: opencost.AccumulateOptionDay,
			input:      string(opencost.AccumulateOptionWeek),
			expected:   opencost.AccumulateOptionWeek,
		},
		{
			name:       "accumulate none with explicit accumulateBy",
			accumulate: opencost.AccumulateOptionNone,
			input:      string(opencost.AccumulateOptionHour),
			expected:   opencost.AccumulateOptionHour,
		},
		{
			name:       "accumulateBy none is valid",
			accumulate: opencost.AccumulateOptionWeek,
			input:      "none",
			expected:   opencost.AccumulateOptionNone,
		},
		{
			name:       "accumulateBy all is valid",
			accumulate: opencost.AccumulateOptionNone,
			input:      "all",
			expected:   opencost.AccumulateOptionAll,
		},
		{
			name:       "accumulateBy normalizes case",
			accumulate: opencost.AccumulateOptionNone,
			input:      "Week",
			expected:   opencost.AccumulateOptionWeek,
		},
		{
			name:       "accumulateBy quarter is valid",
			accumulate: opencost.AccumulateOptionNone,
			input:      string(opencost.AccumulateOptionQuarter),
			expected:   opencost.AccumulateOptionQuarter,
		},
		{
			name:       "accumulate quarter is preserved",
			accumulate: opencost.AccumulateOptionQuarter,
			input:      "",
			expected:   opencost.AccumulateOptionQuarter,
		},
		{
			name:       "invalid accumulateBy is flagged",
			accumulate: opencost.AccumulateOptionNone,
			input:      "nonsense",
			expected:   opencost.AccumulateOptionNone,
			expectErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAccumulateOption(tc.accumulate, tc.input)
			if tc.expectErr && err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !tc.expectErr && err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestResolveAccumulateFromQuery_BackwardCompatibleTruthyValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "true supported", input: "true"},
		{name: "all supported", input: "all"},
		{name: "1 supported", input: "1"},
		{name: "t supported", input: "t"},
		{name: "TRUE supported", input: "TRUE"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			values := url.Values{}
			values.Set("accumulate", tc.input)
			qp := httputil.NewQueryParams(values)
			got := resolveAccumulateFromQuery(qp)
			if got != opencost.AccumulateOptionAll {
				t.Fatalf("expected %q for %q, got %q", opencost.AccumulateOptionAll, tc.input, got)
			}
		})
	}
}

func TestResolveStepForAccumulate(t *testing.T) {
	tests := []struct {
		name         string
		step         time.Duration
		accumulateBy opencost.AccumulateOption
		expected     time.Duration
	}{
		{
			name:         "none keeps requested step",
			step:         14 * 24 * time.Hour,
			accumulateBy: opencost.AccumulateOptionNone,
			expected:     14 * 24 * time.Hour,
		},
		{
			name:         "day uses hourly step",
			step:         14 * 24 * time.Hour,
			accumulateBy: opencost.AccumulateOptionDay,
			expected:     time.Hour,
		},
		{
			name:         "day keeps daily step",
			step:         24 * time.Hour,
			accumulateBy: opencost.AccumulateOptionDay,
			expected:     24 * time.Hour,
		},
		{
			name:         "week uses daily step",
			step:         14 * 24 * time.Hour,
			accumulateBy: opencost.AccumulateOptionWeek,
			expected:     24 * time.Hour,
		},
		{
			name:         "week keeps weekly step",
			step:         7 * 24 * time.Hour,
			accumulateBy: opencost.AccumulateOptionWeek,
			expected:     7 * 24 * time.Hour,
		},
		{
			name:         "quarter uses daily step",
			step:         7 * 24 * time.Hour,
			accumulateBy: opencost.AccumulateOptionQuarter,
			expected:     24 * time.Hour,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStepForAccumulate(tc.step, tc.accumulateBy)
			if got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestResolveDefaultStepFromAccumulate(t *testing.T) {
	window := opencost.NewClosedWindow(
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
	)

	tests := []struct {
		name         string
		accumulateBy opencost.AccumulateOption
		expected     time.Duration
	}{
		{
			name:         "none defaults to window duration",
			accumulateBy: opencost.AccumulateOptionNone,
			expected:     window.Duration(),
		},
		{
			name:         "day defaults to daily",
			accumulateBy: opencost.AccumulateOptionDay,
			expected:     24 * time.Hour,
		},
		{
			name:         "week defaults to weekly",
			accumulateBy: opencost.AccumulateOptionWeek,
			expected:     7 * 24 * time.Hour,
		},
		{
			name:         "month defaults to daily",
			accumulateBy: opencost.AccumulateOptionMonth,
			expected:     24 * time.Hour,
		},
		{
			name:         "all defaults to window duration",
			accumulateBy: opencost.AccumulateOptionAll,
			expected:     window.Duration(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDefaultStepFromAccumulate(window, tc.accumulateBy)
			if got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestResolveStepFromQuery(t *testing.T) {
	window := opencost.NewClosedWindow(
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
	)

	tests := []struct {
		name         string
		stepRaw      string
		accumulateBy opencost.AccumulateOption
		expected     time.Duration
		expectErr    bool
	}{
		{
			name:         "unset step defaults from weekly accumulate",
			stepRaw:      "",
			accumulateBy: opencost.AccumulateOptionWeek,
			expected:     7 * 24 * time.Hour,
		},
		{
			name:         "monthly step keyword supported",
			stepRaw:      "month",
			accumulateBy: opencost.AccumulateOptionNone,
			expected:     24 * time.Hour,
		},
		{
			name:         "weekly step keyword supported",
			stepRaw:      "week",
			accumulateBy: opencost.AccumulateOptionWeek,
			expected:     7 * 24 * time.Hour,
		},
		{
			name:         "day shorthand duration supported",
			stepRaw:      "1d",
			accumulateBy: opencost.AccumulateOptionNone,
			expected:     24 * time.Hour,
		},
		{
			name:         "week shorthand duration supported",
			stepRaw:      "1w",
			accumulateBy: opencost.AccumulateOptionNone,
			expected:     7 * 24 * time.Hour,
		},
		{
			name:         "invalid duration errors",
			stepRaw:      "not-a-duration",
			accumulateBy: opencost.AccumulateOptionNone,
			expectErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			values := url.Values{}
			if tc.stepRaw != "" {
				values.Set("step", tc.stepRaw)
			}
			qp := httputil.NewQueryParams(values)
			got, err := resolveStepFromQuery(qp, window, tc.accumulateBy)
			if tc.expectErr && err == nil {
				t.Fatalf("expected error but got nil")
			}
			if tc.expectErr {
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestWeeklyAccumulateTwoWeeksProducesTwoSets(t *testing.T) {
	start := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC) // Sunday
	end := start.Add(14 * 24 * time.Hour)
	requestedStep := end.Sub(start)

	accumulateBy, err := resolveAccumulateOption(opencost.AccumulateOptionNone, string(opencost.AccumulateOptionWeek))
	if err != nil {
		t.Fatalf("unexpected error resolving accumulate option: %s", err)
	}
	step := resolveStepForAccumulate(requestedStep, accumulateBy)
	if step != 24*time.Hour {
		t.Fatalf("expected daily step for weekly accumulation, got %v", step)
	}

	asr := opencost.NewAllocationSetRange()
	for ts := start; ts.Before(end); ts = ts.Add(step) {
		next := ts.Add(step)
		as := opencost.NewAllocationSet(ts, next)
		as.Set(opencost.NewMockUnitAllocation("workload", ts, step, nil))
		asr.Append(as)
	}

	weekly, err := asr.Accumulate(opencost.AccumulateOptionWeek)
	if err != nil {
		t.Fatalf("unexpected weekly accumulate error: %s", err)
	}

	if len(weekly.Allocations) != 2 {
		t.Fatalf("expected 2 weekly sets from 2 weeks of data, got %d", len(weekly.Allocations))
	}

	for i, as := range weekly.Allocations {
		if got := as.Window.Duration(); got != 7*24*time.Hour {
			t.Fatalf("set %d expected 7d window, got %s", i, got)
		}
	}
}

func TestResolveQueryWindowForAccumulate_WeekRoundsToCalendarWeeks(t *testing.T) {
	start := time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC) // Monday
	end := start.Add(14 * 24 * time.Hour)
	window := opencost.NewClosedWindow(start, end)

	got, err := resolveQueryWindowForAccumulate(window, opencost.AccumulateOptionWeek)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	expectedStart := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC) // Sunday
	expectedEnd := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)  // Sunday after 3 calendar weeks
	if !got.Start().Equal(expectedStart) {
		t.Fatalf("expected rounded start %s, got %s", expectedStart, got.Start())
	}
	if !got.End().Equal(expectedEnd) {
		t.Fatalf("expected rounded end %s, got %s", expectedEnd, got.End())
	}
}

func TestTrimAllocationSetRangeToRequestWindow(t *testing.T) {
	requestStart := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	requestEnd := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	requestWindow := opencost.NewClosedWindow(requestStart, requestEnd)

	before := opencost.NewAllocationSet(
		time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
	)
	overlap := opencost.NewAllocationSet(
		time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC),
	)
	inside := opencost.NewAllocationSet(
		time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
	)
	after := opencost.NewAllocationSet(
		time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
	)

	asr := opencost.NewAllocationSetRange(before, overlap, inside, after)
	asr.FromStore = "test-store"
	trimmed := trimAllocationSetRangeToRequestWindow(asr, requestWindow)

	if len(trimmed.Allocations) != 2 {
		t.Fatalf("expected 2 overlapping sets, got %d", len(trimmed.Allocations))
	}
	if !trimmed.Allocations[0].Start().Equal(overlap.Start()) {
		t.Fatalf("expected first set to start at %s, got %s", overlap.Start(), trimmed.Allocations[0].Start())
	}
	if !trimmed.Allocations[1].Start().Equal(inside.Start()) {
		t.Fatalf("expected second set to start at %s, got %s", inside.Start(), trimmed.Allocations[1].Start())
	}
	if trimmed.FromStore != asr.FromStore {
		t.Fatalf("expected FromStore to be preserved")
	}
}
