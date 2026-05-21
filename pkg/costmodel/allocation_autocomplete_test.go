package costmodel

import (
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
)

func TestBuildAllocationAutocompleteQueriesRawAllocationsAndReturnsSortedUniqueValues(t *testing.T) {
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	window := opencost.NewClosedWindow(start, end)

	called := false
	values, err := buildAllocationAutocomplete(window, "label", "app", `namespace:"default"`, func(window opencost.Window, step time.Duration, aggregate []string, includeIdle, idleByNode, includeProportionalAssetResourceCosts, includeAggregatedMetadata, sharedLoadBalancer bool, accumulateBy opencost.AccumulateOption, shareIdle bool, filterString string) (*opencost.AllocationSetRange, error) {
		called = true
		if step != window.Duration() {
			t.Fatalf("expected query step %s, got %s", window.Duration(), step)
		}
		if aggregate != nil {
			t.Fatalf("expected nil aggregate for raw allocation autocomplete, got %#v", aggregate)
		}
		if filterString != `namespace:"default"` {
			t.Fatalf("expected filter to pass through, got %q", filterString)
		}

		return mockAllocationSetRangeForAutocomplete(start, end), nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatalf("expected allocation query to be called")
	}

	expected := []string{"app", "app.kubernetes.io/name"}
	if len(values) != len(expected) {
		t.Fatalf("expected %d values, got %d: %#v", len(expected), len(values), values)
	}
	for i := range expected {
		if values[i] != expected[i] {
			t.Fatalf("expected values[%d]=%q, got %q", i, expected[i], values[i])
		}
	}
}

func TestCollectAllocationAutocompleteValuesSupportsMultipleFields(t *testing.T) {
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	asr := mockAllocationSetRangeForAutocomplete(start, end)

	testCases := []struct {
		field    string
		search   string
		expected []string
	}{
		{field: allocationAutocompleteFieldCluster, expected: []string{"cluster-a", "cluster-b"}},
		{field: allocationAutocompleteFieldNode, expected: []string{"node-a", "node-b"}},
		{field: allocationAutocompleteFieldNamespace, expected: []string{"default", "kube-system"}},
		{field: allocationAutocompleteFieldController, expected: []string{"cost-analyzer", "metrics-server"}},
		{field: allocationAutocompleteFieldControllerKind, expected: []string{"deployment"}},
		{field: allocationAutocompleteFieldProviderID, expected: []string{"node-a-id", "node-b-id"}},
		{field: allocationAutocompleteFieldService, expected: []string{"kube-system/metrics-server", "kubecost/cost-analyzer"}},
		{field: allocationAutocompleteFieldAnnotation, search: "team", expected: []string{"team"}},
	}

	for _, tc := range testCases {
		t.Run(tc.field, func(t *testing.T) {
			values := collectAllocationAutocompleteValues(asr, tc.field, tc.search)
			if len(values) != len(tc.expected) {
				t.Fatalf("expected %d values, got %d: %#v", len(tc.expected), len(values), values)
			}
			for i := range tc.expected {
				if values[i] != tc.expected[i] {
					t.Fatalf("expected values[%d]=%q, got %q", i, tc.expected[i], values[i])
				}
			}
		})
	}
}

func TestNormalizeAllocationAutocompleteFieldRejectsUnsupportedField(t *testing.T) {
	if _, err := normalizeAllocationAutocompleteField("owner"); err == nil {
		t.Fatalf("expected unsupported field to return error")
	}
}

func mockAllocationSetRangeForAutocomplete(start, end time.Time) *opencost.AllocationSetRange {
	window := opencost.NewClosedWindow(start, end)
	set := &opencost.AllocationSet{
		Window: window,
		Allocations: map[string]*opencost.Allocation{
			"cluster-a/default/cost-analyzer/pod-a/container-a": {
				Name: "cluster-a/default/cost-analyzer/pod-a/container-a",
				Properties: &opencost.AllocationProperties{
					Cluster:        "cluster-a",
					Node:           "node-a",
					Namespace:      "default",
					Pod:            "pod-a",
					Container:      "container-a",
					Controller:     "cost-analyzer",
					ControllerKind: "deployment",
					ProviderID:     "node-a-id",
					Services:       []string{"kubecost/cost-analyzer"},
					Labels: map[string]string{
						"app":                    "cost-analyzer",
						"app.kubernetes.io/name": "cost-analyzer",
					},
					NamespaceLabels: map[string]string{
						"team": "platform",
					},
					Annotations: map[string]string{
						"owner": "finops",
					},
				},
			},
			"cluster-b/kube-system/metrics-server/pod-b/container-b": {
				Name: "cluster-b/kube-system/metrics-server/pod-b/container-b",
				Properties: &opencost.AllocationProperties{
					Cluster:        "cluster-b",
					Node:           "node-b",
					Namespace:      "kube-system",
					Pod:            "pod-b",
					Container:      "container-b",
					Controller:     "metrics-server",
					ControllerKind: "deployment",
					ProviderID:     "node-b-id",
					Services:       []string{"kube-system/metrics-server"},
					Labels: map[string]string{
						"app": "metrics-server",
					},
					NamespaceAnnotations: map[string]string{
						"team": "platform",
					},
				},
			},
		},
	}

	return opencost.NewAllocationSetRange(set)
}
