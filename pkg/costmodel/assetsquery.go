package costmodel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	assetfilter "github.com/opencost/opencost/core/pkg/filter/asset"
	"github.com/opencost/opencost/core/pkg/filter/ast"
	"github.com/opencost/opencost/core/pkg/filter/matcher"
	"github.com/opencost/opencost/core/pkg/opencost"
)

const defaultAssetGraphLimit = 25
const defaultAssetAggregate = string(opencost.AssetTypeProp)

type assetSetComputer func(start, end time.Time) (*opencost.AssetSet, error)

type AssetGraphResponse struct {
	Chart []AssetGraphDataSet `json:"chart"`
}

type AssetGraphDataSet struct {
	Start     time.Time        `json:"start"`
	End       time.Time        `json:"end"`
	TotalCost float64          `json:"totalCost"`
	Items     []AssetGraphItem `json:"items"`
}

type AssetGraphItem struct {
	Name string  `json:"name"`
	Cost float64 `json:"cost"`
}

func parseAssetMatcher(filterString string) (opencost.AssetMatcher, error) {
	if filterString == "" {
		return &matcher.AllPass[opencost.Asset]{}, nil
	}

	parser := assetfilter.NewAssetFilterParser()
	tree, err := parser.Parse(filterString)
	if err != nil {
		return nil, fmt.Errorf("err parsing filter %q: %w", filterString, err)
	}

	compiler := opencost.NewAssetMatchCompiler()
	filter, err := compiler.Compile(tree)
	if err != nil {
		return nil, fmt.Errorf("err compiling filter '%s': %w", ast.ToPreOrderShortString(tree), err)
	}
	if filter == nil {
		return nil, fmt.Errorf("unexpected nil filter")
	}

	return filter, nil
}

func buildAssetFilterString(filterString, clusterString string) string {
	clusterString = strings.TrimSpace(clusterString)
	if clusterString == "" {
		return filterString
	}

	clusters := strings.Split(clusterString, ",")
	values := make([]string, 0, len(clusters))
	for _, cluster := range clusters {
		cluster = strings.TrimSpace(cluster)
		if cluster == "" {
			continue
		}
		values = append(values, fmt.Sprintf("%q", cluster))
	}

	if len(values) == 0 {
		return filterString
	}

	clusterFilter := fmt.Sprintf("cluster:%s", strings.Join(values, ","))
	if strings.TrimSpace(filterString) == "" {
		return clusterFilter
	}

	return fmt.Sprintf("(%s) + (%s)", clusterFilter, filterString)
}

func filterAssetSet(assetSet *opencost.AssetSet, filter opencost.AssetMatcher) *opencost.AssetSet {
	if assetSet == nil || filter == nil {
		return assetSet
	}

	result := opencost.NewAssetSet(assetSet.Start(), assetSet.End())
	for key, asset := range assetSet.Assets {
		if filter.Matches(asset) {
			result.Assets[key] = asset.Clone()
		}
	}

	return result
}

func computeAssetSet(window opencost.Window, filterString string, compute assetSetComputer) (*opencost.AssetSet, error) {
	filter, err := parseAssetMatcher(filterString)
	if err != nil {
		return nil, err
	}

	assetSet, err := compute(*window.Start(), *window.End())
	if err != nil {
		return nil, fmt.Errorf("error computing asset set: %w", err)
	}

	return filterAssetSet(assetSet, filter), nil
}

func computeAssetSetRange(window opencost.Window, step time.Duration, filterString string, compute assetSetComputer) (*opencost.AssetSetRange, error) {
	if window.IsOpen() || window.IsNegative() {
		return nil, fmt.Errorf("illegal window: %s", window)
	}
	if step <= 0 {
		return nil, fmt.Errorf("step must be positive: %s", step)
	}

	filter, err := parseAssetMatcher(filterString)
	if err != nil {
		return nil, err
	}

	asr := opencost.NewAssetSetRange()
	stepStart := *window.Start()
	windowEnd := *window.End()

	for stepStart.Before(windowEnd) {
		stepEnd := stepStart.Add(step)
		if stepEnd.After(windowEnd) {
			stepEnd = windowEnd
		}

		assetSet, err := compute(stepStart, stepEnd)
		if err != nil {
			return nil, fmt.Errorf("error computing asset set for %s: %w", opencost.NewClosedWindow(stepStart, stepEnd), err)
		}

		asr.Append(filterAssetSet(assetSet, filter))
		stepStart = stepEnd
	}

	return asr, nil
}

func normalizeAssetAggregate(aggregate string) (string, error) {
	if aggregate == "" {
		return defaultAssetAggregate, nil
	}

	prop, err := opencost.ParseAssetProperty(aggregate)
	if err != nil {
		return "", err
	}

	return string(prop), nil
}

func assetQueryStep(accumulate opencost.AccumulateOption) (time.Duration, error) {
	switch accumulate {
	case opencost.AccumulateOptionHour:
		return time.Hour, nil
	case opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth, opencost.AccumulateOptionAll:
		return 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported accumulate option for asset set range: %s", accumulate)
	}
}

func queryAggregatedAssetSetRange(window opencost.Window, filterString, aggregate string, accumulate opencost.AccumulateOption, compute assetSetComputer) (*opencost.AssetSetRange, error) {
	var asr *opencost.AssetSetRange
	var err error

	aggregate, err = normalizeAssetAggregate(aggregate)
	if err != nil {
		return nil, err
	}

	switch accumulate {
	case opencost.AccumulateOptionNone:
		assetSet, computeErr := computeAssetSet(window, filterString, compute)
		if computeErr != nil {
			return nil, computeErr
		}
		asr = opencost.NewAssetSetRange(assetSet)
	default:
		step, stepErr := assetQueryStep(accumulate)
		if stepErr != nil {
			return nil, stepErr
		}

		asr, err = computeAssetSetRange(window, step, filterString, compute)
		if err != nil {
			return nil, err
		}
		asr, err = asr.Accumulate(accumulate)
		if err != nil {
			return nil, fmt.Errorf("error accumulating assets by %s: %w", accumulate, err)
		}
	}

	if err := asr.AggregateBy([]string{aggregate}, nil); err != nil {
		return nil, fmt.Errorf("error aggregating assets by %s: %w", aggregate, err)
	}

	return asr, nil
}

func querySteppedAssetSetRange(window opencost.Window, filterString, aggregate string, step time.Duration, compute assetSetComputer) (*opencost.AssetSetRange, error) {
	aggregate, err := normalizeAssetAggregate(aggregate)
	if err != nil {
		return nil, err
	}

	asr, err := computeAssetSetRange(window, step, filterString, compute)
	if err != nil {
		return nil, err
	}

	if err := asr.AggregateBy([]string{aggregate}, nil); err != nil {
		return nil, fmt.Errorf("error aggregating assets by %s: %w", aggregate, err)
	}

	return asr, nil
}

func buildAssetAggregateResponse(asr *opencost.AssetSetRange) []map[string]opencost.Asset {
	if asr == nil {
		return []map[string]opencost.Asset{}
	}

	resp := make([]map[string]opencost.Asset, 0, asr.Length())
	for _, assetSet := range asr.Assets {
		entry := map[string]opencost.Asset{}
		if assetSet != nil {
			for key, asset := range assetSet.Assets {
				entry[key] = asset
			}
		}
		resp = append(resp, entry)
	}

	return resp
}

func buildAssetGraphResponse(asr *opencost.AssetSetRange, offset, limit int) *AssetGraphResponse {
	if asr == nil {
		return &AssetGraphResponse{Chart: []AssetGraphDataSet{}}
	}
	if offset < 0 {
		offset = 0
	}

	resp := &AssetGraphResponse{
		Chart: make([]AssetGraphDataSet, 0, asr.Length()),
	}

	for _, assetSet := range asr.Assets {
		dataSet := AssetGraphDataSet{}
		if assetSet != nil {
			dataSet.Start = assetSet.Start()
			dataSet.End = assetSet.End()
			dataSet.TotalCost = assetSet.TotalCost()
			items := make([]AssetGraphItem, 0, len(assetSet.Assets))
			for key, asset := range assetSet.Assets {
				items = append(items, AssetGraphItem{
					Name: key,
					Cost: asset.TotalCost(),
				})
			}

			sort.Slice(items, func(i, j int) bool {
				if items[i].Cost == items[j].Cost {
					return items[i].Name < items[j].Name
				}
				return items[i].Cost > items[j].Cost
			})

			if offset < len(items) {
				items = items[offset:]
			} else {
				items = []AssetGraphItem{}
			}
			if len(items) > limit {
				items = items[:limit]
			}
			dataSet.Items = items
		}
		resp.Chart = append(resp.Chart, dataSet)
	}

	return resp
}
