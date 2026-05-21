package costmodel

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
)

func TestIsCSVRequest(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected bool
	}{
		{"no format param", "", false},
		{"format=json", "format=json", false},
		{"format=csv", "format=csv", true},
		{"format=CSV (case sensitive)", "format=CSV", false},
		{"format=csv with other params", "window=7d&format=csv", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := url.Parse("http://localhost?" + tt.query)
			qp := httputil.NewQueryParams(u.Query())
			got := isCSVRequest(qp)
			if got != tt.expected {
				t.Errorf("isCSVRequest() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestSetCSVDownloadHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	setCSVDownloadHeaders(w, "test-file.csv")

	ct := w.Header().Get("Content-Type")
	if ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/csv; charset=utf-8")
	}

	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition missing 'attachment': %q", cd)
	}
	if !strings.Contains(cd, `filename="test-file.csv"`) {
		t.Errorf("Content-Disposition missing filename: %q", cd)
	}
}

func TestWriteUTF8BOM(t *testing.T) {
	var buf strings.Builder
	if err := writeUTF8BOM(&buf); err != nil {
		t.Fatalf("writeUTF8BOM() returned error: %v", err)
	}
	got := buf.String()
	expected := string([]byte{0xEF, 0xBB, 0xBF})
	if got != expected {
		t.Errorf("writeUTF8BOM() = %x, want %x", []byte(got), []byte(expected))
	}
}

func TestBuildCSVFilename(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 8, 0, 0, 0, 0, time.UTC)
	w := opencost.NewClosedWindow(start, end)

	tests := []struct {
		name       string
		reportType string
		window     opencost.Window
		aggregate  string
		expected   string
	}{
		{
			name:       "allocation with namespace aggregate",
			reportType: "allocation",
			window:     w,
			aggregate:  "namespace",
			expected:   "allocation-20240101-20240108-namespace.csv",
		},
		{
			name:       "allocation with multiple aggregates",
			reportType: "allocation",
			window:     w,
			aggregate:  "namespace,label:app",
			expected:   "allocation-20240101-20240108-namespace-label-app.csv",
		},
		{
			name:       "asset with empty aggregate → all",
			reportType: "asset",
			window:     w,
			aggregate:  "",
			expected:   "asset-20240101-20240108-all.csv",
		},
		{
			name:       "asset with type aggregate",
			reportType: "asset",
			window:     w,
			aggregate:  "type",
			expected:   "asset-20240101-20240108-type.csv",
		},
		{
			name:       "efficiency with pod aggregate",
			reportType: "efficiency",
			window:     w,
			aggregate:  "pod",
			expected:   "efficiency-20240101-20240108-pod.csv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCSVFilename(tt.reportType, tt.window, tt.aggregate)
			if got != tt.expected {
				t.Errorf("buildCSVFilename() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSanitizeFilenamePart(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"namespace", "namespace"},
		{"namespace,label:app", "namespace-label-app"},
		{"namespace, label:app", "namespace-label-app"},
		{"a,b:c d", "a-b-c-d"},
		{"--leading--", "leading"},
		{"trailing--", "trailing"},
		{"multi--dash", "multi-dash"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilenamePart(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeFilenamePart(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
