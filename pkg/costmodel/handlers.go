package costmodel

import (
	"fmt"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"github.com/opencost/opencost/pkg/carbon"
	"github.com/opencost/opencost/pkg/env"
)

// ComputeAssetsHandler returns the assets from the CostModel.
func (a *Accesses) ComputeAssetsHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")
	if resp, ok := a.getQueryCacheResponse("assets", r); ok {
		w.Write(resp)
		return
	}

	qp := httputil.NewQueryParams(r.URL.Query())

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	filterString := buildAssetFilterString(qp.Get("filter", ""), qp.Get("cluster", ""))
	aggregate := qp.Get("aggregate", "")
	stepRaw := qp.Get("step", "")
	step := qp.GetDuration("step", 0)
	accumulate := opencost.ParseAccumulate(qp.Get("accumulate", ""))

	if aggregate != "" && aggregate != string(opencost.AssetTypeProp) {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: only %q is supported", opencost.AssetTypeProp), http.StatusBadRequest)
		return
	}

	if aggregate == "" {
		switch {
		case stepRaw != "":
			http.Error(w, "'step' requires 'aggregate'", http.StatusBadRequest)
			return
		case qp.Get("accumulate", "") != "":
			http.Error(w, "'accumulate' requires 'aggregate'", http.StatusBadRequest)
			return
		}

		assetSet, err := a.ComputeAssetsFromCostmodel(window, filterString)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting assets: %s", err), http.StatusInternalServerError)
			return
		}

		resp := WrapData(assetSet, nil)
		a.setQueryCacheResponse("assets", r, resp)
		w.Write(resp)
		return
	}

	if stepRaw != "" {
		if accumulate != opencost.AccumulateOptionNone {
			http.Error(w, "'step' cannot be combined with 'accumulate'", http.StatusBadRequest)
			return
		}
		if step <= 0 {
			http.Error(w, fmt.Sprintf("Invalid 'step' parameter: %q", stepRaw), http.StatusBadRequest)
			return
		}

		asr, err := querySteppedAssetSetRange(window, filterString, defaultAssetAggregate, step, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting stepped assets: %s", err), http.StatusInternalServerError)
			return
		}

		resp := WrapData(buildAssetAggregateResponse(asr), nil)
		a.setQueryCacheResponse("assets", r, resp)
		w.Write(resp)
		return
	}

	switch accumulate {
	case opencost.AccumulateOptionNone, opencost.AccumulateOptionAll, opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth:
	default:
		http.Error(w, fmt.Sprintf("Invalid 'accumulate' parameter for /assets: %q", qp.Get("accumulate", "")), http.StatusBadRequest)
		return
	}

	asr, err := queryAggregatedAssetSetRange(window, filterString, defaultAssetAggregate, accumulate, a.Model.ComputeAssets)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error getting aggregated assets: %s", err), http.StatusInternalServerError)
		return
	}

	resp := WrapData(buildAssetAggregateResponse(asr), nil)
	a.setQueryCacheResponse("assets", r, resp)
	w.Write(resp)
}

// ComputeAssetsGraphHandler returns graph-ready aggregated asset costs.
func (a *Accesses) ComputeAssetsGraphHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	aggregate, err := normalizeAssetAggregate(qp.Get("aggregate", ""))
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	stepRaw := qp.Get("step", "")
	step := qp.GetDuration("step", 0)
	accumulateRaw := qp.Get("accumulate", "day")
	accumulate := opencost.ParseAccumulate(accumulateRaw)

	if stepRaw != "" {
		if qp.Get("accumulate", "") != "" {
			http.Error(w, "'step' cannot be combined with 'accumulate'", http.StatusBadRequest)
			return
		}
		if step <= 0 {
			http.Error(w, fmt.Sprintf("Invalid 'step' parameter: %q", stepRaw), http.StatusBadRequest)
			return
		}
	} else {
		switch accumulate {
		case opencost.AccumulateOptionHour, opencost.AccumulateOptionDay, opencost.AccumulateOptionWeek, opencost.AccumulateOptionMonth:
		default:
			http.Error(w, fmt.Sprintf("Invalid 'accumulate' parameter for /assets/graph: %q", accumulateRaw), http.StatusBadRequest)
			return
		}
	}

	offset := qp.GetInt("offset", 0)
	if offset < 0 {
		http.Error(w, fmt.Sprintf("Invalid 'offset' parameter: %d", offset), http.StatusBadRequest)
		return
	}

	limit := qp.GetInt("limit", defaultAssetGraphLimit)
	if limit < 0 {
		http.Error(w, fmt.Sprintf("Invalid 'limit' parameter: %d", limit), http.StatusBadRequest)
		return
	}

	filterString := buildAssetFilterString(qp.Get("filter", ""), qp.Get("cluster", ""))

	var asr *opencost.AssetSetRange
	if stepRaw != "" {
		asr, err = querySteppedAssetSetRange(window, filterString, aggregate, step, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting stepped asset graph data: %s", err), http.StatusInternalServerError)
			return
		}
	} else {
		asr, err = queryAggregatedAssetSetRange(window, filterString, aggregate, accumulate, a.Model.ComputeAssets)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error getting asset graph data: %s", err), http.StatusInternalServerError)
			return
		}
	}

	w.Write(WrapData(buildAssetGraphResponse(asr, offset, limit), nil))
}

// ComputeAssetsCarbonHandler returns carbon estimates for assets.
func (a *Accesses) ComputeAssetsCarbonHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	qp := httputil.NewQueryParams(r.URL.Query())

	// Window is a required field describing the window of time over which to
	// compute allocation data.
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	filterString := buildAssetFilterString(qp.Get("filter", ""), qp.Get("cluster", ""))

	assetSet, err := a.ComputeAssetsFromCostmodel(window, filterString)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error getting assets: %s", err), http.StatusInternalServerError)
		return
	}

	carbonEstimates, err := carbon.RelateCarbonAssets(assetSet)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error relating carbon assets: %s", err), http.StatusInternalServerError)
		return
	}

	WriteData(w, carbonEstimates, nil)
}

func (a *Accesses) ComputeAssetsFromCostmodel(window opencost.Window, filterString string) (*opencost.AssetSet, error) {
	return computeAssetSet(window, filterString, a.Model.ComputeAssets)
}
