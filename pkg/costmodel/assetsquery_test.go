package costmodel

import (
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
)

func TestQueryAggregatedAssetSetRangeAccumulateAll(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	asr, err := queryAggregatedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
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

func TestBuildAssetGraphResponseSortOffsetAndLimit(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	asr, err := queryAggregatedAssetSetRange(
		opencost.NewClosedWindow(start, end),
		"",
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

	item := graph.Chart[0].Items[0]
	if item.Name != "Disk" {
		t.Fatalf("expected Disk as second highest cost item, got %s", item.Name)
	}
	if item.Cost != 2 {
		t.Fatalf("expected Disk cost 2, got %f", item.Cost)
	}
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
