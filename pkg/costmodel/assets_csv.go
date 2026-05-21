package costmodel

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
)

// writeAssetDetailCSV writes individual assets from an AssetSet as CSV.
// Columns: AssetType, Name, Cluster, Node, Provider, Category, Service, Cost,
// Adjustment, TotalCost, WindowStart, WindowEnd.
// maxRows limits the number of data rows written (excluding header). If maxRows
// <= 0, no limit is applied. Returns true if the output was truncated.
func writeAssetDetailCSV(w io.Writer, as *opencost.AssetSet, maxRows int) (bool, error) {
	fmtFloat := func(f float64) string {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}

	header := []string{
		"AssetType", "Name", "Cluster", "Node", "Provider", "Category",
		"Service", "Cost", "Adjustment", "TotalCost", "WindowStart", "WindowEnd",
	}

	csvWriter := csv.NewWriter(w)
	if err := csvWriter.Write(header); err != nil {
		return false, fmt.Errorf("failed to write CSV header: %w", err)
	}

	if as == nil {
		csvWriter.Flush()
		return false, csvWriter.Error()
	}

	start := as.Start()
	end := as.End()
	startStr := start.Format(time.RFC3339)
	endStr := end.Format(time.RFC3339)

	// Sort asset keys for stable output ordering.
	keys := make([]string, 0, len(as.Assets))
	for k := range as.Assets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rowCount := 0
	truncated := false
	for _, k := range keys {
		if maxRows > 0 && rowCount >= maxRows {
			truncated = true
			break
		}
		asset := as.Assets[k]
		if asset == nil {
			continue
		}
		props := asset.GetProperties()
		row := []string{
			asset.Type().String(),
			safeProp(props, func(p *opencost.AssetProperties) string { return p.Name }),
			safeProp(props, func(p *opencost.AssetProperties) string { return p.Cluster }),
			assetNode(asset),
			safeProp(props, func(p *opencost.AssetProperties) string { return p.Provider }),
			safeProp(props, func(p *opencost.AssetProperties) string { return p.Category }),
			safeProp(props, func(p *opencost.AssetProperties) string { return p.Service }),
			fmtFloat(asset.TotalCost() - asset.GetAdjustment()),
			fmtFloat(asset.GetAdjustment()),
			fmtFloat(asset.TotalCost()),
			startStr,
			endStr,
		}
		if err := csvWriter.Write(row); err != nil {
			return truncated, fmt.Errorf("failed to write CSV row: %w", err)
		}
		rowCount++
	}

	csvWriter.Flush()
	return truncated, csvWriter.Error()
}

// writeAssetAggregateCSV writes aggregated asset data (grouped by type) as CSV.
// Columns: Type, Count, Cost, Adjustment, TotalCost, WindowStart, WindowEnd.
// maxRows limits the number of data rows written (excluding header). If maxRows
// <= 0, no limit is applied. Returns true if the output was truncated.
// rawASR may be provided to compute accurate Count values from the un-aggregated
// original data. If nil, Count defaults to 1 per aggregated entry.
func writeAssetAggregateCSV(w io.Writer, asr *opencost.AssetSetRange, rawASR *opencost.AssetSetRange, maxRows int) (bool, error) {
	fmtFloat := func(f float64) string {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}

	header := []string{
		"Type", "Count", "Cost", "Adjustment", "TotalCost", "WindowStart", "WindowEnd",
	}

	csvWriter := csv.NewWriter(w)
	if err := csvWriter.Write(header); err != nil {
		return false, fmt.Errorf("failed to write CSV header: %w", err)
	}

	rowCount := 0
	truncated := false
	for i, as := range asr.Assets {
		if as == nil {
			continue
		}
		start := as.Start()
		end := as.End()
		startStr := start.Format(time.RFC3339)
		endStr := end.Format(time.RFC3339)

		// Build type counts from raw data for the corresponding window, if available.
		typeCounts := map[string]int{}
		if rawASR != nil && i < len(rawASR.Assets) && rawASR.Assets[i] != nil {
			for _, rawAsset := range rawASR.Assets[i].Assets {
				if rawAsset != nil {
					typeCounts[rawAsset.Type().String()]++
				}
			}
		}

		// Sort asset keys for stable output ordering.
		keys := make([]string, 0, len(as.Assets))
		for k := range as.Assets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if maxRows > 0 && rowCount >= maxRows {
				truncated = true
				break
			}
			asset := as.Assets[k]
			if asset == nil {
				continue
			}
			count := 1
			if c, ok := typeCounts[asset.Type().String()]; ok && c > 0 {
				count = c
			}
			row := []string{
				asset.Type().String(),
				strconv.Itoa(count),
				fmtFloat(asset.TotalCost() - asset.GetAdjustment()),
				fmtFloat(asset.GetAdjustment()),
				fmtFloat(asset.TotalCost()),
				startStr,
				endStr,
			}
			if err := csvWriter.Write(row); err != nil {
				return truncated, fmt.Errorf("failed to write CSV row: %w", err)
			}
			rowCount++
		}
		if truncated {
			break
		}
	}

	csvWriter.Flush()
	return truncated, csvWriter.Error()
}

// writeAssetGraphCSV writes asset graph data as CSV.
// Columns: WindowStart, WindowEnd, Aggregate, Name, Cost, TotalCost.
//
// The offset and limit parameters are applied per AssetSet (time bucket), matching
// the JSON handler's pagination semantics. Items within each bucket are sorted by
// cost descending, then name ascending, before offset/limit is applied.
//
// maxRows limits the total number of data rows written (excluding header) across
// all AssetSets. If maxRows <= 0, no limit is applied. Returns true if the output
// was truncated.
func writeAssetGraphCSV(w io.Writer, asr *opencost.AssetSetRange, offset, limit, maxRows int) (bool, error) {
	fmtFloat := func(f float64) string {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}

	header := []string{
		"WindowStart", "WindowEnd", "Aggregate", "Name", "Cost", "TotalCost",
	}

	csvWriter := csv.NewWriter(w)
	if err := csvWriter.Write(header); err != nil {
		return false, fmt.Errorf("failed to write CSV header: %w", err)
	}

	rowCount := 0
	truncated := false
	for _, as := range asr.Assets {
		if as == nil {
			continue
		}
		startStr := as.Start().Format(time.RFC3339)
		endStr := as.End().Format(time.RFC3339)
		totalCost := fmtFloat(as.TotalCost())

		// Determine the aggregate dimension from the aggregation keys.
		aggregate := ""
		if len(as.AggregationKeys) > 0 {
			aggregate = as.AggregationKeys[0]
		}

		// Collect items for sorting, then apply offset/limit (matching JSON handler semantics).
		type item struct {
			name string
			cost float64
			asset opencost.Asset
		}
		items := make([]item, 0, len(as.Assets))
		for k, asset := range as.Assets {
			if asset == nil {
				continue
			}
			items = append(items, item{name: k, cost: asset.TotalCost(), asset: asset})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].cost == items[j].cost {
				return items[i].name < items[j].name
			}
			return items[i].cost > items[j].cost
		})

		if offset < len(items) {
			items = items[offset:]
		} else {
			items = []item{}
		}
		if len(items) > limit {
			items = items[:limit]
		}

		for _, it := range items {
			if maxRows > 0 && rowCount >= maxRows {
				truncated = true
				break
			}
			row := []string{
				startStr,
				endStr,
				aggregate,
				it.name,
				fmtFloat(it.cost),
				totalCost,
			}
			if err := csvWriter.Write(row); err != nil {
				return truncated, fmt.Errorf("failed to write CSV row: %w", err)
			}
			rowCount++
		}
		if truncated {
			break
		}
	}

	csvWriter.Flush()
	return truncated, csvWriter.Error()
}

// safeProp safely extracts a property from AssetProperties, returning "" if nil.
func safeProp(props *opencost.AssetProperties, getter func(*opencost.AssetProperties) string) string {
	if props == nil {
		return ""
	}
	return getter(props)
}

// assetNode returns the node name if the asset is of type Node, otherwise
// attempts to get node info from labels.
func assetNode(a opencost.Asset) string {
	if a == nil {
		return ""
	}
	if a.Type() == opencost.NodeAssetType {
		return safeProp(a.GetProperties(), func(p *opencost.AssetProperties) string { return p.Name })
	}
	// Try common node label keys
	labels := a.GetLabels()
	if labels != nil {
		if v, ok := labels["node"]; ok {
			return v
		}
		if v, ok := labels["kubernetes.io/hostname"]; ok {
			return v
		}
	}
	return ""
}
