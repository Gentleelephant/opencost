package costmodel

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"github.com/opencost/opencost/pkg/env"
)

type allocationQueryFunc func(window opencost.Window, step time.Duration, aggregate []string, includeIdle, idleByNode, includeProportionalAssetResourceCosts, includeAggregatedMetadata, sharedLoadBalancer bool, accumulateBy opencost.AccumulateOption, shareIdle bool, filterString string) (*opencost.AllocationSetRange, error)

const (
	allocationAutocompleteFieldLabel          = "label"
	allocationAutocompleteFieldAnnotation     = "annotation"
	allocationAutocompleteFieldCluster        = "cluster"
	allocationAutocompleteFieldNode           = "node"
	allocationAutocompleteFieldNamespace      = "namespace"
	allocationAutocompleteFieldPod            = "pod"
	allocationAutocompleteFieldContainer      = "container"
	allocationAutocompleteFieldController     = "controller"
	allocationAutocompleteFieldControllerKind = "controllerkind"
	allocationAutocompleteFieldProviderID     = "providerid"
	allocationAutocompleteFieldService        = "service"
)

// ComputeAllocationAutocompleteHandler returns autocomplete candidates for allocation fields.
// @Summary      查询分配字段自动补全候选项
// @Tags         Allocation
// @Description  兼容 Kubecost 的 allocation autocomplete 接口。当前支持 label、annotation、cluster、node、namespace、pod、container、controller、controllerKind、providerID、service。
// @Param        window  query  string  true   "时间窗口。必填。"
// @Param        field   query  string  true   "字段名。支持 label、annotation、cluster、node、namespace、pod、container、controller、controllerKind、providerID、service。"
// @Param        search  query  string  false  "搜索关键字，按包含关系过滤候选项。"
// @Param        filter  query  string  false  "分配过滤条件。"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /allocation/autocomplete [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/allocation/autocomplete [get]
func (a *Accesses) ComputeAllocationAutocompleteHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	if resp, ok := a.getQueryCacheResponse("allocation-autocomplete", r); ok {
		w.Write(resp)
		return
	}

	qp := httputil.NewQueryParams(r.URL.Query())
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	if a == nil || a.Model == nil {
		http.Error(w, "allocation model is not initialized", http.StatusInternalServerError)
		return
	}

	values, err := buildAllocationAutocomplete(window, qp.Get("field", ""), qp.Get("search", ""), qp.Get("filter", ""), a.Model.QueryAllocation)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp := WrapData(map[string]any{
		"data": values,
	}, nil)
	a.setQueryCacheResponseWithTTL("allocation-autocomplete", r, resp, cacheTTLForWindow(&window))
	w.Write(resp)
}

func buildAllocationAutocomplete(window opencost.Window, field, search, filter string, query allocationQueryFunc) ([]string, error) {
	canonicalField, err := normalizeAllocationAutocompleteField(field)
	if err != nil {
		return nil, fmt.Errorf("invalid 'field' parameter: %w", err)
	}

	if query == nil {
		return nil, fmt.Errorf("allocation query function is nil")
	}

	asr, err := query(window, window.Duration(), nil, false, false, false, false, false, opencost.AccumulateOptionNone, false, filter)
	if err != nil {
		return nil, err
	}

	return collectAllocationAutocompleteValues(asr, canonicalField, search), nil
}

func normalizeAllocationAutocompleteField(field string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case allocationAutocompleteFieldLabel:
		return allocationAutocompleteFieldLabel, nil
	case allocationAutocompleteFieldAnnotation:
		return allocationAutocompleteFieldAnnotation, nil
	case allocationAutocompleteFieldCluster:
		return allocationAutocompleteFieldCluster, nil
	case allocationAutocompleteFieldNode:
		return allocationAutocompleteFieldNode, nil
	case allocationAutocompleteFieldNamespace:
		return allocationAutocompleteFieldNamespace, nil
	case allocationAutocompleteFieldPod:
		return allocationAutocompleteFieldPod, nil
	case allocationAutocompleteFieldContainer:
		return allocationAutocompleteFieldContainer, nil
	case allocationAutocompleteFieldController, "controllername":
		return allocationAutocompleteFieldController, nil
	case allocationAutocompleteFieldControllerKind:
		return allocationAutocompleteFieldControllerKind, nil
	case allocationAutocompleteFieldProviderID:
		return allocationAutocompleteFieldProviderID, nil
	case allocationAutocompleteFieldService, "services":
		return allocationAutocompleteFieldService, nil
	default:
		return "", fmt.Errorf("unsupported field %q", field)
	}
}

func collectAllocationAutocompleteValues(asr *opencost.AllocationSetRange, field, search string) []string {
	search = strings.ToLower(strings.TrimSpace(search))
	seen := make(map[string]struct{})

	addValue := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if search != "" && !strings.Contains(strings.ToLower(value), search) {
			return
		}
		seen[value] = struct{}{}
	}

	addMapKeys := func(values map[string]string) {
		for key := range values {
			addValue(key)
		}
	}

	if asr != nil {
		for _, as := range asr.Allocations {
			if as == nil {
				continue
			}
			for _, alloc := range as.Allocations {
				if alloc == nil || alloc.Properties == nil {
					continue
				}

				props := alloc.Properties
				switch field {
				case allocationAutocompleteFieldLabel:
					addMapKeys(props.Labels)
					addMapKeys(props.NamespaceLabels)
				case allocationAutocompleteFieldAnnotation:
					addMapKeys(props.Annotations)
					addMapKeys(props.NamespaceAnnotations)
				case allocationAutocompleteFieldCluster:
					addValue(props.Cluster)
				case allocationAutocompleteFieldNode:
					addValue(props.Node)
				case allocationAutocompleteFieldNamespace:
					addValue(props.Namespace)
				case allocationAutocompleteFieldPod:
					addValue(props.Pod)
				case allocationAutocompleteFieldContainer:
					addValue(props.Container)
				case allocationAutocompleteFieldController:
					addValue(props.Controller)
				case allocationAutocompleteFieldControllerKind:
					addValue(props.ControllerKind)
				case allocationAutocompleteFieldProviderID:
					addValue(props.ProviderID)
				case allocationAutocompleteFieldService:
					for _, service := range props.Services {
						addValue(service)
					}
				}
			}
		}
	}

	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
