package costmodel

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
)

func TestAllocStepCacheKey(t *testing.T) {
	start := time.Unix(1000, 0)
	end := time.Unix(2000, 0)
	resolution := time.Minute

	key1 := allocStepCacheKey(start, end, resolution)
	key2 := allocStepCacheKey(start, end, resolution)
	assert.Equal(t, key1, key2)

	key3 := allocStepCacheKey(start.Add(time.Second), end, resolution)
	assert.NotEqual(t, key1, key3)

	key4 := allocStepCacheKey(start, end, 2*time.Minute)
	assert.NotEqual(t, key1, key4)
}

func TestAllocStepCacheTTL(t *testing.T) {
	now := time.Now()

	assert.Equal(t, 10*time.Minute, allocStepCacheTTL(now.Add(-2*time.Hour)))
	assert.Equal(t, 30*time.Second, allocStepCacheTTL(now.Add(-30*time.Minute)))
}

func TestCloneNodeMap(t *testing.T) {
	src := map[nodeKey]*nodePricing{
		{Cluster: "cluster-1", Node: "node-1"}: {
			Name:         "node-1",
			NodeType:     "m5.large",
			CostPerCPUHr: 0.1,
		},
	}

	dst := cloneNodeMap(src)
	assert.Len(t, dst, 1)

	for nk := range src {
		src[nk].NodeType = "modified"
		assert.Equal(t, "m5.large", dst[nk].NodeType)
	}

	assert.Nil(t, cloneNodeMap(nil))
}

func TestAllocStepCacheHitReturnsClone(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 1, 2, 0, 0, 0, 0, time.UTC)
	resolution := time.Duration(0)

	original := opencost.NewAllocationSet(start, end)
	original.Set(&opencost.Allocation{
		Name:         "test-alloc",
		Properties:   &opencost.AllocationProperties{Pod: "test-pod", Namespace: "test-ns"},
		CPUCoreHours: 2,
	})

	stepCache := cache.New(5*time.Minute, 10*time.Minute)
	key := allocStepCacheKey(start, end, resolution)
	stepCache.Set(key, &allocStepCacheValue{allocSet: original.Clone()}, cache.DefaultExpiration)

	cm := &CostModel{allocStepCache: stepCache}

	as1, nodeMap1, err1 := cm.computeAllocation(start, end)
	assert.NoError(t, err1)
	assert.Nil(t, nodeMap1)

	alloc1 := as1.Get("test-alloc")
	assert.NotNil(t, alloc1)
	alloc1.CPUCoreHours = 99

	as2, _, err2 := cm.computeAllocation(start, end)
	assert.NoError(t, err2)
	assert.Equal(t, 2.0, as2.Get("test-alloc").CPUCoreHours)
}

func TestAllocStepCacheReturnsNodeMap(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 1, 2, 0, 0, 0, 0, time.UTC)
	resolution := time.Duration(0)
	nk := nodeKey{Cluster: "cluster-1", Node: "node-1"}

	stepCache := cache.New(5*time.Minute, 10*time.Minute)
	stepCache.Set(allocStepCacheKey(start, end, resolution), &allocStepCacheValue{
		allocSet: opencost.NewAllocationSet(start, end),
		nodeMap: map[nodeKey]*nodePricing{
			nk: {
				Name:         "node-1",
				NodeType:     "m5.large",
				CostPerCPUHr: 0.1,
			},
		},
	}, cache.DefaultExpiration)

	cm := &CostModel{allocStepCache: stepCache}

	_, nodeMap1, err := cm.computeAllocation(start, end)
	assert.NoError(t, err)
	assert.Equal(t, "m5.large", nodeMap1[nk].NodeType)

	nodeMap1[nk].CostPerCPUHr = 999
	_, nodeMap2, err := cm.computeAllocation(start, end)
	assert.NoError(t, err)
	assert.Equal(t, 0.1, nodeMap2[nk].CostPerCPUHr)
}

func TestAllocStepCacheConcurrentAccess(t *testing.T) {
	start := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 1, 2, 0, 0, 0, 0, time.UTC)
	resolution := time.Duration(0)

	original := opencost.NewAllocationSet(start, end)
	for i := 0; i < 100; i++ {
		original.Set(&opencost.Allocation{
			Name:       fmt.Sprintf("alloc-%d", i),
			Properties: &opencost.AllocationProperties{Pod: fmt.Sprintf("pod-%d", i), Namespace: "ns"},
		})
	}

	stepCache := cache.New(5*time.Minute, 10*time.Minute)
	stepCache.Set(allocStepCacheKey(start, end, resolution), &allocStepCacheValue{
		allocSet: original.Clone(),
	}, cache.DefaultExpiration)

	cm := &CostModel{allocStepCache: stepCache}

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			as, _, err := cm.computeAllocation(start, end)
			if err != nil {
				errs <- err
				return
			}
			if as.Length() != 100 {
				errs <- fmt.Errorf("expected 100 allocations, got %d", as.Length())
				return
			}
			as.Insert(&opencost.Allocation{Name: "extra", Properties: &opencost.AllocationProperties{Pod: "extra"}})
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("unexpected concurrent access error: %v", err)
	}
}
