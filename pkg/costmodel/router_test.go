package costmodel

import (
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/stretchr/testify/assert"
)

func TestCacheTTLForWindow(t *testing.T) {
	now := time.Now()
	historicalEnd := now.Add(-2 * time.Hour)
	realtimeEnd := now.Add(-30 * time.Minute)
	futureEnd := now.Add(time.Hour)

	tests := []struct {
		name     string
		window   *opencost.Window
		expected time.Duration
	}{
		{name: "nil window returns default expiration", window: nil, expected: 0},
		{
			name: "nil end returns default expiration",
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
			name: "near realtime caps at 30s",
			window: func() *opencost.Window {
				w := opencost.NewWindow(&realtimeEnd, &realtimeEnd)
				return &w
			}(),
			expected: 30 * time.Second,
		},
		{
			name: "future end is treated as realtime",
			window: func() *opencost.Window {
				w := opencost.NewWindow(&futureEnd, &futureEnd)
				return &w
			}(),
			expected: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, cacheTTLForWindow(tt.window))
		})
	}
}

func TestCacheTTLForWindowRespectsGlobalConfig(t *testing.T) {
	now := time.Now()
	historicalEnd := now.Add(-2 * time.Hour)
	realtimeEnd := now.Add(-30 * time.Minute)
	wHistorical := opencost.NewWindow(&historicalEnd, &historicalEnd)
	wRealtime := opencost.NewWindow(&realtimeEnd, &realtimeEnd)

	t.Run("historical uses longer configured ttl", func(t *testing.T) {
		t.Setenv(queryCacheTTLEnvVar, "600")
		assert.Equal(t, 10*time.Minute, cacheTTLForWindow(&wHistorical))
	})

	t.Run("realtime uses shorter configured ttl", func(t *testing.T) {
		t.Setenv(queryCacheTTLEnvVar, "10")
		assert.Equal(t, 10*time.Second, cacheTTLForWindow(&wRealtime))
	})

	t.Run("realtime caps long configured ttl at 30s", func(t *testing.T) {
		t.Setenv(queryCacheTTLEnvVar, "600")
		assert.Equal(t, 30*time.Second, cacheTTLForWindow(&wRealtime))
	})

	t.Run("historical floors short configured ttl at 5m", func(t *testing.T) {
		t.Setenv(queryCacheTTLEnvVar, "30")
		assert.Equal(t, 5*time.Minute, cacheTTLForWindow(&wHistorical))
	})
}
