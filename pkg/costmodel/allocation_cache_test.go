package costmodel

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/stretchr/testify/assert"
)

func TestAllocStepCacheKey(t *testing.T) {
	start := time.Unix(1000, 0)
	end := time.Unix(2000, 0)
	resolution := 1 * time.Minute

	key1 := allocStepCacheKey(start, end, resolution)
	key2 := allocStepCacheKey(start, end, resolution)
	assert.Equal(t, key1, key2, "same inputs should produce same key")

	key3 := allocStepCacheKey(start.Add(1*time.Second), end, resolution)
	assert.NotEqual(t, key1, key3, "different start should produce different key")

	key4 := allocStepCacheKey(start, end, 2*time.Minute)
	assert.NotEqual(t, key1, key4, "different resolution should produce different key")
}

func TestAllocStepCacheTTL(t *testing.T) {
	now := time.Now()
	historicalEnd := now.Add(-2 * time.Hour)
	realtimeEnd := now.Add(-30 * time.Minute)

	t.Run("historical window uses 10m TTL", func(t *testing.T) {
		assert.Equal(t, 10*time.Minute, allocStepCacheTTL(historicalEnd))
	})
	t.Run("near-realtime window uses 30s TTL", func(t *testing.T) {
		assert.Equal(t, 30*time.Second, allocStepCacheTTL(realtimeEnd))
	})
}

func TestCloneNodeMap(t *testing.T) {
	src := make(map[nodeKey]*nodePricing)

	nk := nodeKey{Cluster: "cluster-1", Node: "node-1"}
	src[nk] = &nodePricing{
		Name:         "node-1",
		NodeType:     "m5.large",
		CostPerCPUHr: 0.1,
	}

	dst := cloneNodeMap(src)
	assert.Len(t, dst, 1)
	assert.Equal(t, "m5.large", dst[nk].NodeType)

	// Mutate source, verify destination is independent
	src[nk].NodeType = "modified"
	assert.Equal(t, "m5.large", dst[nk].NodeType, "cloneNodeMap should deep copy values")

	// nil input should return nil
	assert.Nil(t, cloneNodeMap(nil))
}

func TestAllocStepCache_HitReturnsClone(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 1, 2, 0, 0, 0, 0, time.UTC)
	resolution := 1 * time.Hour

	original := opencost.NewAllocationSet(start, end)
	original.Set(&opencost.Allocation{
		Name:          "test-alloc",
		Properties:    &opencost.AllocationProperties{Pod: "test-pod", Namespace: "test-ns"},
		CPUCoreHours:  2.0,
	})

	stepCache := cache.New(5*time.Minute, 10*time.Minute)
	key := allocStepCacheKey(start, end, resolution)
	stepCache.Set(key, &allocStepCacheValue{
		allocSet: original.Clone(),
		nodeMap:  nil,
	}, cache.DefaultExpiration)

	cm := &CostModel{
		allocStepCache:             stepCache,
		MaxPrometheusQueryDuration: 24 * time.Hour,
	}

	// First call: should hit the cache
	allocSet1, nodeMap1, err1 := cm.computeAllocation(start, end, resolution)
	assert.NoError(t, err1)
	assert.NotNil(t, allocSet1)
	assert.Nil(t, nodeMap1, "nil nodeMap should return nil on cache hit")
	assert.Equal(t, 1, allocSet1.Length())

	// Verify the returned object is a deep copy, not the cached original
	alloc1 := allocSet1.Get("test-alloc")
	assert.NotNil(t, alloc1)
	assert.Equal(t, 2.0, alloc1.CPUCoreHours)

	// Mutate the returned object
	alloc1.CPUCoreHours = 99.0

	// Second call: should hit cache again, return fresh clone
	allocSet2, _, err2 := cm.computeAllocation(start, end, resolution)
	assert.NoError(t, err2)
	alloc2 := allocSet2.Get("test-alloc")
	assert.NotNil(t, alloc2)
	assert.Equal(t, 2.0, alloc2.CPUCoreHours, "cache hit should return unmutated data (deep copy)")
}

func TestAllocStepCache_ReturnsNodeMap(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 1, 2, 0, 0, 0, 0, time.UTC)
	resolution := 1 * time.Hour

	original := opencost.NewAllocationSet(start, end)
	nk := nodeKey{Cluster: "cluster-1", Node: "node-1"}
	nodeMap := map[nodeKey]*nodePricing{
		nk: {
			Name:         "node-1",
			NodeType:     "m5.large",
			CostPerCPUHr: 0.1,
		},
	}

	stepCache := cache.New(5*time.Minute, 10*time.Minute)
	key := allocStepCacheKey(start, end, resolution)
	stepCache.Set(key, &allocStepCacheValue{
		allocSet: original.Clone(),
		nodeMap:  cloneNodeMap(nodeMap),
	}, cache.DefaultExpiration)

	cm := &CostModel{
		allocStepCache:             stepCache,
		MaxPrometheusQueryDuration: 24 * time.Hour,
	}

	allocSet, cachedNodeMap, err := cm.computeAllocation(start, end, resolution)
	assert.NoError(t, err)
	assert.NotNil(t, allocSet)

	assert.Len(t, cachedNodeMap, 1, "cache hit should return nodeMap")
	assert.Equal(t, "m5.large", cachedNodeMap[nk].NodeType)

	// Mutate returned nodeMap, verify cache is not polluted
	cachedNodeMap[nk].CostPerCPUHr = 999.0

	_, cachedNodeMap2, _ := cm.computeAllocation(start, end, resolution)
	assert.Equal(t, 0.1, cachedNodeMap2[nk].CostPerCPUHr, "cache hit should return unmutated nodeMap")
}

func TestAllocStepCache_CachePollutionIsolation(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 1, 2, 0, 0, 0, 0, time.UTC)
	resolution := 1 * time.Hour

	original := opencost.NewAllocationSet(start, end)
	original.Set(&opencost.Allocation{
		Name:       "alloc-1",
		Properties: &opencost.AllocationProperties{Pod: "pod-1", Namespace: "ns-1"},
	})

	stepCache := cache.New(5*time.Minute, 10*time.Minute)
	key := allocStepCacheKey(start, end, resolution)
	stepCache.Set(key, &allocStepCacheValue{
		allocSet: original.Clone(),
	}, cache.DefaultExpiration)

	cm := &CostModel{
		allocStepCache:             stepCache,
		MaxPrometheusQueryDuration: 24 * time.Hour,
	}

	// Hit 1: mutate the returned set (Insert a new allocation)
	as1, _, _ := cm.computeAllocation(start, end, resolution)
	as1.Insert(&opencost.Allocation{
		Name:       "injected",
		Properties: &opencost.AllocationProperties{Pod: "injected"},
	})

	// Hit 2: should still return the original data without the injected allocation
	as2, _, err := cm.computeAllocation(start, end, resolution)
	assert.NoError(t, err)
	assert.Equal(t, 1, as2.Length(), "cache should not contain injected allocation")
	assert.Nil(t, as2.Get("injected"))
}

func TestAllocStepCache_ConcurrentAccess(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 1, 2, 0, 0, 0, 0, time.UTC)
	resolution := 1 * time.Hour

	original := opencost.NewAllocationSet(start, end)
	for i := 0; i < 100; i++ {
		original.Set(&opencost.Allocation{
			Name:       fmt.Sprintf("alloc-%d", i),
			Properties: &opencost.AllocationProperties{Pod: fmt.Sprintf("pod-%d", i), Namespace: "ns"},
		})
	}

	stepCache := cache.New(5*time.Minute, 10*time.Minute)
	key := allocStepCacheKey(start, end, resolution)
	stepCache.Set(key, &allocStepCacheValue{
		allocSet: original.Clone(),
	}, cache.DefaultExpiration)

	cm := &CostModel{
		allocStepCache:             stepCache,
		MaxPrometheusQueryDuration: 24 * time.Hour,
	}

	var wg sync.WaitGroup
	errors := make(chan error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			as, _, err := cm.computeAllocation(start, end, resolution)
			if err != nil {
				errors <- err
				return
			}
			if as.Length() != 100 {
				errors <- fmt.Errorf("expected 100 allocations, got %d", as.Length())
				return
			}
			// Mutate each returned copy differently
			as.Insert(&opencost.Allocation{
				Name:       "extra",
				Properties: &opencost.AllocationProperties{Pod: "extra"},
			})
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent access error: %v", err)
	}
}
