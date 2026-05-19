package costmodel

import (
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/stretchr/testify/assert"
)

func TestCacheTTLForWindow(t *testing.T) {
	now := time.Now()
	// For the window to be "historical", the *end* must be > 1h in the past
	historicalEnd := now.Add(-2 * time.Hour)
	// For "near-realtime", the end is recent (or in the future)
	realtimeEnd := now.Add(-30 * time.Minute)
	futureEnd := now.Add(1 * time.Hour)

	// defaultQueryCacheTTL = 60s (from router.go const)
	// With the default 60s base TTL:
	//   - historical: max(60s, 5m) = 5m
	//   - near-realtime: min(60s, 30s) = 30s

	tests := []struct {
		name     string
		window   *opencost.Window
		expected time.Duration
	}{
		{
			name:     "nil window returns DefaultExpiration",
			window:   nil,
			expected: 0, // cache.DefaultExpiration
		},
		{
			name: "nil end returns DefaultExpiration",
			window: func() *opencost.Window {
				w := opencost.NewWindow(&historicalEnd, nil)
				return &w
			}(),
			expected: 0,
		},
		{
			name: "historical window returns at least 5m",
			window: func() *opencost.Window {
				w := opencost.NewWindow(&historicalEnd, &historicalEnd)
				return &w
			}(),
			expected: 5 * time.Minute,
		},
		{
			name: "near-realtime window caps at 30s",
			window: func() *opencost.Window {
				w := opencost.NewWindow(&realtimeEnd, &realtimeEnd)
				return &w
			}(),
			expected: 30 * time.Second,
		},
		{
			name: "window ending in future is near-realtime",
			window: func() *opencost.Window {
				w := opencost.NewWindow(&futureEnd, &futureEnd)
				return &w
			}(),
			expected: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cacheTTLForWindow(tt.window)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestCacheTTLForWindow_RespectsGlobalConfig(t *testing.T) {
	now := time.Now()
	historicalEnd := now.Add(-2 * time.Hour)
	realtimeEnd := now.Add(-30 * time.Minute)
	wHistorical := opencost.NewWindow(&historicalEnd, &historicalEnd)
	wRealtime := opencost.NewWindow(&realtimeEnd, &realtimeEnd)

	t.Run("historical window uses base TTL if longer than 5m", func(t *testing.T) {
		t.Setenv(queryCacheTTLEnvVar, "600") // 10 minutes
		got := cacheTTLForWindow(&wHistorical)
		assert.Equal(t, 10*time.Minute, got)
	})

	t.Run("near-realtime window uses base TTL if shorter than 30s", func(t *testing.T) {
		t.Setenv(queryCacheTTLEnvVar, "10") // 10 seconds
		got := cacheTTLForWindow(&wRealtime)
		assert.Equal(t, 10*time.Second, got)
	})

	t.Run("near-realtime window caps at 30s even with long base TTL", func(t *testing.T) {
		t.Setenv(queryCacheTTLEnvVar, "600") // 10 minutes
		got := cacheTTLForWindow(&wRealtime)
		assert.Equal(t, 30*time.Second, got)
	})

	t.Run("historical window uses 5m when base TTL is shorter", func(t *testing.T) {
		t.Setenv(queryCacheTTLEnvVar, "30") // 30 seconds
		got := cacheTTLForWindow(&wHistorical)
		assert.Equal(t, 5*time.Minute, got)
	})
}
