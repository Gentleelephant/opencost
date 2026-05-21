package costmodel

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
)

// isCSVRequest checks if the request's "format" query parameter equals "csv".
func isCSVRequest(qp httputil.QueryParams) bool {
	return qp.Get("format", "") == "csv"
}

// setCSVDownloadHeaders sets Content-Type (with charset) and Content-Disposition
// headers for CSV download.
func setCSVDownloadHeaders(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Transfer-Encoding", "chunked")
}

// writeUTF8BOM writes a UTF-8 BOM (byte order mark) to the writer so that
// Excel recognizes the CSV as UTF-8 encoded.
func writeUTF8BOM(w io.Writer) error {
	// UTF-8 BOM: EF BB BF
	_, err := w.Write([]byte{0xEF, 0xBB, 0xBF})
	return err
}

// buildCSVFilename generates a download filename in the form:
//
//	<reportType>-<window>-<aggregate>.csv
//
// Examples:
//   - allocation-20240101-20240108-namespace.csv
//   - asset-20240101-20240108-type.csv
//   - efficiency-20240101-20240108-pod.csv
func buildCSVFilename(reportType string, window opencost.Window, aggregate string) string {
	parts := []string{reportType}

	// Format window as YYYYMMDD-YYYYMMDD
	if s := window.Start(); s != nil {
		parts = append(parts, s.Format("20060102"))
	} else {
		parts = append(parts, "beginning")
	}
	if e := window.End(); e != nil {
		parts = append(parts, e.Format("20060102"))
	} else {
		parts = append(parts, "now")
	}

	agg := sanitizeFilenamePart(aggregate)
	if agg == "" {
		agg = "all"
	}
	parts = append(parts, agg)

	return strings.Join(parts, "-") + ".csv"
}

var sanitizeRe = regexp.MustCompile(`[,\s:]+`)

// sanitizeFilenamePart replaces commas, colons, and spaces with hyphens and
// collapses consecutive hyphens into a single one.
func sanitizeFilenamePart(s string) string {
	if s == "" {
		return ""
	}
	result := sanitizeRe.ReplaceAllString(s, "-")
	// Collapse consecutive hyphens
	result = collapseHyphens(result)
	// Trim leading/trailing hyphens
	result = strings.Trim(result, "-")
	return result
}

// collapseHyphens replaces runs of consecutive hyphens with a single hyphen.
func collapseHyphens(s string) string {
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

// formatOpenCostWindowShort formats a window's start and end as YYYYMMDD.
// Convenience helper for building CSV filenames or display purposes.
func formatOpenCostWindowShort(w opencost.Window) string {
	var start, end time.Time
	if s := w.Start(); s != nil {
		start = *s
	}
	if e := w.End(); e != nil {
		end = *e
	}
	return fmt.Sprintf("%s-%s", start.Format("20060102"), end.Format("20060102"))
}
