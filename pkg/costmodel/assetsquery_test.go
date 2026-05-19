package costmodel

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/core/pkg/opencost"
)

func TestQueryAggregatedAssetSetRangeAccumulateAll(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	asr, err := queryAggregatedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
		defaultAssetAggregate,
		opencost.AccumulateOptionAll,
		mockAssetComputer(map[int64]*opencost.AssetSet{
			start.Unix():                     testAssetSet(start, start.Add(24*time.Hour), 10, 2, 0),
			start.Add(24 * time.Hour).Unix(): testAssetSet(start.Add(24*time.Hour), end, 3, 1, 0),
		}),
	)
	if err != nil {
		t.Fatalf("queryAggregatedAssetSetRange returned error: %v", err)
	}

	if got := asr.Length(); got != 1 {
		t.Fatalf("expected 1 accumulated asset set, got %d", got)
	}

	resp := buildAssetAggregateResponse(asr)
	if len(resp) != 1 {
		t.Fatalf("expected 1 response entry, got %d", len(resp))
	}

	assertAssetCost(t, resp[0], "Node", 13)
	assertAssetCost(t, resp[0], "Disk", 3)
	assertAssetCost(t, resp[0], "Network", 0)
}

func TestQueryAggregatedAssetSetRangeDayAndFilter(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	asr, err := queryAggregatedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		`assetType:"node"`,
		defaultAssetAggregate,
		opencost.AccumulateOptionDay,
		mockAssetComputer(map[int64]*opencost.AssetSet{
			start.Unix():                     testAssetSet(start, start.Add(24*time.Hour), 10, 2, 0),
			start.Add(24 * time.Hour).Unix(): testAssetSet(start.Add(24*time.Hour), end, 3, 1, 0),
		}),
	)
	if err != nil {
		t.Fatalf("queryAggregatedAssetSetRange returned error: %v", err)
	}

	if got := asr.Length(); got != 2 {
		t.Fatalf("expected 2 daily asset sets, got %d", got)
	}

	resp := buildAssetAggregateResponse(asr)
	if len(resp) != 2 {
		t.Fatalf("expected 2 response entries, got %d", len(resp))
	}

	for i, entry := range resp {
		if len(entry) != 1 {
			t.Fatalf("entry %d expected 1 asset type after filter, got %d", i, len(entry))
		}
		if _, ok := entry["Node"]; !ok {
			t.Fatalf("entry %d expected Node key after filter", i)
		}
	}
}

func TestBuildAssetFilterString_WithClusterOnly(t *testing.T) {
	got := buildAssetFilterString("", "cluster-a, cluster-b")
	want := `cluster:"cluster-a","cluster-b"`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildAssetFilterString_WithClusterAndFilter(t *testing.T) {
	got := buildAssetFilterString(`assetType:"node"`, "cluster-a")
	want := `(cluster:"cluster-a") + (assetType:"node")`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestQueryAggregatedAssetSetRangeFilterByCluster(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	asr, err := queryAggregatedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		`cluster:"cluster-b"`,
		"name",
		opencost.AccumulateOptionDay,
		mockAssetComputer(map[int64]*opencost.AssetSet{
			start.Unix(): testAssetSetWithClusters(start, end),
		}),
	)
	if err != nil {
		t.Fatalf("queryAggregatedAssetSetRange returned error: %v", err)
	}

	resp := buildAssetAggregateResponse(asr)
	if len(resp) != 1 {
		t.Fatalf("expected 1 response entry, got %d", len(resp))
	}

	entry := resp[0]
	if len(entry) != 1 {
		t.Fatalf("expected 1 asset after cluster filter, got %d", len(entry))
	}

	assertAssetCost(t, entry, "node-b", 7)
}

func TestBuildAssetGraphResponseSortOffsetAndLimit(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	asr, err := queryAggregatedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
		defaultAssetAggregate,
		opencost.AccumulateOptionDay,
		mockAssetComputer(map[int64]*opencost.AssetSet{
			start.Unix(): testAssetSet(start, end, 10, 2, 1),
		}),
	)
	if err != nil {
		t.Fatalf("queryAggregatedAssetSetRange returned error: %v", err)
	}

	graph := buildAssetGraphResponse(asr, 1, 1)
	if len(graph.Chart) != 1 {
		t.Fatalf("expected 1 chart entry, got %d", len(graph.Chart))
	}
	if got := len(graph.Chart[0].Items); got != 1 {
		t.Fatalf("expected 1 chart item after offset/limit, got %d", got)
	}
	if graph.Chart[0].TotalCost != 13 {
		t.Fatalf("expected total cost 13, got %f", graph.Chart[0].TotalCost)
	}

	item := graph.Chart[0].Items[0]
	if item.Name != "Disk" {
		t.Fatalf("expected Disk as second highest cost item, got %s", item.Name)
	}
	if item.Cost != 2 {
		t.Fatalf("expected Disk cost 2, got %f", item.Cost)
	}
}

func TestQueryAggregatedAssetSetRangeHourReturnsHourlyBuckets(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	sets := map[int64]*opencost.AssetSet{}
	for i := 0; i < 48; i++ {
		bucketStart := start.Add(time.Duration(i) * time.Hour)
		bucketEnd := bucketStart.Add(time.Hour)
		sets[bucketStart.Unix()] = testAssetSet(bucketStart, bucketEnd, 1, 0, 0)
	}

	asr, err := queryAggregatedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
		defaultAssetAggregate,
		opencost.AccumulateOptionHour,
		mockAssetComputer(sets),
	)
	if err != nil {
		t.Fatalf("queryAggregatedAssetSetRange returned error: %v", err)
	}

	if got := asr.Length(); got != 48 {
		t.Fatalf("expected 48 hourly asset sets, got %d", got)
	}

	for i, assetSet := range asr.Assets {
		if assetSet == nil {
			t.Fatalf("expected non-nil asset set at index %d", i)
		}

		expectedStart := start.Add(time.Duration(i) * time.Hour)
		expectedEnd := expectedStart.Add(time.Hour)
		if assetSet.Start() != expectedStart {
			t.Fatalf("bucket %d expected start %s, got %s", i, expectedStart, assetSet.Start())
		}
		if assetSet.End() != expectedEnd {
			t.Fatalf("bucket %d expected end %s, got %s", i, expectedEnd, assetSet.End())
		}
	}
}

func TestQuerySteppedAssetSetRangeReturnsTwelveHourBuckets(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	sets := map[int64]*opencost.AssetSet{}
	for i := 0; i < 4; i++ {
		bucketStart := start.Add(time.Duration(i) * 12 * time.Hour)
		bucketEnd := bucketStart.Add(12 * time.Hour)
		sets[bucketStart.Unix()] = testAssetSet(bucketStart, bucketEnd, float64(i+1), 0, 0)
	}

	asr, err := querySteppedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
		defaultAssetAggregate,
		12*time.Hour,
		mockAssetComputer(sets),
	)
	if err != nil {
		t.Fatalf("querySteppedAssetSetRange returned error: %v", err)
	}

	if got := asr.Length(); got != 4 {
		t.Fatalf("expected 4 stepped asset sets, got %d", got)
	}

	for i, assetSet := range asr.Assets {
		expectedStart := start.Add(time.Duration(i) * 12 * time.Hour)
		expectedEnd := expectedStart.Add(12 * time.Hour)
		if assetSet.Start() != expectedStart {
			t.Fatalf("bucket %d expected start %s, got %s", i, expectedStart, assetSet.Start())
		}
		if assetSet.End() != expectedEnd {
			t.Fatalf("bucket %d expected end %s, got %s", i, expectedEnd, assetSet.End())
		}
	}
}

func TestQuerySteppedAssetSetRangeReturnsTrailingPartialBucket(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Hour)

	sets := map[int64]*opencost.AssetSet{
		start.Unix():                     testAssetSet(start, start.Add(24*time.Hour), 10, 0, 0),
		start.Add(24 * time.Hour).Unix(): testAssetSet(start.Add(24*time.Hour), end, 4, 0, 0),
	}

	asr, err := querySteppedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
		defaultAssetAggregate,
		24*time.Hour,
		mockAssetComputer(sets),
	)
	if err != nil {
		t.Fatalf("querySteppedAssetSetRange returned error: %v", err)
	}

	if got := asr.Length(); got != 2 {
		t.Fatalf("expected 2 stepped asset sets, got %d", got)
	}

	if got := asr.Assets[1].Window.Duration(); got != 6*time.Hour {
		t.Fatalf("expected trailing partial bucket duration 6h, got %s", got)
	}
}

func TestComputeAssetsHandlerRejectsStepWithoutAggregate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/kapis/costwise.wiztelemetry.io/v1alpha1/assets?window=7d&step=12h", nil)
	rr := httptest.NewRecorder()

	(&Accesses{}).ComputeAssetsHandler(rr, req, httprouter.Params{})

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestComputeAssetsHandlerRejectsStepWithAccumulate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/kapis/costwise.wiztelemetry.io/v1alpha1/assets?window=7d&aggregate=type&step=12h&accumulate=day", nil)
	rr := httptest.NewRecorder()

	(&Accesses{}).ComputeAssetsHandler(rr, req, httprouter.Params{})

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestComputeAssetsGraphHandlerRejectsStepWithAccumulate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/kapis/costwise.wiztelemetry.io/v1alpha1/assets/graph?window=7d&aggregate=type&step=12h&accumulate=day", nil)
	rr := httptest.NewRecorder()

	(&Accesses{}).ComputeAssetsGraphHandler(rr, req, httprouter.Params{})

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestBuildAssetGraphResponseSupportsSteppedRange(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	sets := map[int64]*opencost.AssetSet{}
	for i := 0; i < 4; i++ {
		bucketStart := start.Add(time.Duration(i) * 12 * time.Hour)
		bucketEnd := bucketStart.Add(12 * time.Hour)
		sets[bucketStart.Unix()] = testAssetSet(bucketStart, bucketEnd, float64(10+i), 2, 1)
	}

	asr, err := querySteppedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
		defaultAssetAggregate,
		12*time.Hour,
		mockAssetComputer(sets),
	)
	if err != nil {
		t.Fatalf("querySteppedAssetSetRange returned error: %v", err)
	}

	graph := buildAssetGraphResponse(asr, 0, 2)
	if got := len(graph.Chart); got != 4 {
		t.Fatalf("expected 4 chart entries, got %d", got)
	}

	for i, datum := range graph.Chart {
		expectedStart := start.Add(time.Duration(i) * 12 * time.Hour)
		expectedEnd := expectedStart.Add(12 * time.Hour)
		if datum.Start != expectedStart {
			t.Fatalf("datum %d expected start %s, got %s", i, expectedStart, datum.Start)
		}
		if datum.End != expectedEnd {
			t.Fatalf("datum %d expected end %s, got %s", i, expectedEnd, datum.End)
		}
		if len(datum.Items) != 2 {
			t.Fatalf("datum %d expected 2 graph items, got %d", i, len(datum.Items))
		}
	}
}

func TestQueryAggregatedAssetSetRangeAggregateByName(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	asr, err := queryAggregatedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
		"name",
		opencost.AccumulateOptionDay,
		mockAssetComputer(map[int64]*opencost.AssetSet{
			start.Unix(): testAssetSet(start, end, 10, 2, 1),
		}),
	)
	if err != nil {
		t.Fatalf("queryAggregatedAssetSetRange returned error: %v", err)
	}

	resp := buildAssetAggregateResponse(asr)
	if len(resp) != 1 {
		t.Fatalf("expected 1 response entry, got %d", len(resp))
	}

	entry := resp[0]
	assertAssetCost(t, entry, "node", 10)
	assertAssetCost(t, entry, "disk", 2)
	assertAssetCost(t, entry, "network", 1)
}

func mockAssetComputer(sets map[int64]*opencost.AssetSet) assetSetComputer {
	return func(start, end time.Time) (*opencost.AssetSet, error) {
		set, ok := sets[start.Unix()]
		if !ok {
			return opencost.NewAssetSet(start, end), nil
		}
		return set.Clone(), nil
	}
}

func testAssetSet(start, end time.Time, nodeCost, diskCost, networkCost float64) *opencost.AssetSet {
	window := opencost.NewClosedWindow(start, end)

	node := opencost.NewNode("node", "cluster-a", "node-1", start, end, window)
	node.CPUCost = nodeCost

	disk := opencost.NewDisk("disk", "cluster-a", "disk-1", start, end, window)
	disk.Cost = diskCost

	network := opencost.NewNetwork("network", "cluster-a", "network-1", start, end, window)
	network.Cost = networkCost

	return opencost.NewAssetSet(start, end, node, disk, network)
}

func testAssetSetWithClusters(start, end time.Time) *opencost.AssetSet {
	window := opencost.NewClosedWindow(start, end)

	nodeA := opencost.NewNode("node-a", "cluster-a", "node-1", start, end, window)
	nodeA.CPUCost = 10

	nodeB := opencost.NewNode("node-b", "cluster-b", "node-2", start, end, window)
	nodeB.CPUCost = 7

	return opencost.NewAssetSet(start, end, nodeA, nodeB)
}

func assertAssetCost(t *testing.T, entry map[string]opencost.Asset, key string, expected float64) {
	t.Helper()

	asset, ok := entry[key]
	if !ok {
		t.Fatalf("expected key %q in response entry", key)
	}
	if got := asset.TotalCost(); got != expected {
		t.Fatalf("expected %s total cost %f, got %f", key, expected, got)
	}
}
