package costmodel

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"

	"github.com/julienschmidt/httprouter"
	allocationfilter "github.com/opencost/opencost/core/pkg/filter/allocation"
	"github.com/opencost/opencost/core/pkg/log"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util/httputil"
	"github.com/opencost/opencost/pkg/env"
)

// ComputeEfficiencyHandler computes and returns efficiency data.
// Supports format=csv for CSV export.
// @Summary      查询效率优化建议
// @Tags         Efficiency
// @Description  基于请求窗口和聚合维度计算资源效率、推荐请求值和潜在成本节省，支持 CSV 导出。
// @Param        window            query  string   true   "时间窗口。必填。"
// @Param        aggregate         query  string   false  "聚合维度。默认 pod。"
// @Param        filter            query  string   false  "分配过滤条件。"
// @Param        bufferMultiplier  query  number   false  "推荐资源计算缓冲系数。默认 1.2。"
// @Param        format            query  string   false  "返回格式；传 csv 导出 CSV。"
// @Success      200  {object}  costmodel.Response
// @Failure      400  {object}  costmodel.Response
// @Failure      500  {object}  costmodel.Response
// @Router       /efficiency [get]
// @Router       /kapis/costwise.wiztelemetry.io/v1alpha1/efficiency [get]
func (a *Accesses) ComputeEfficiencyHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	qp := httputil.NewQueryParams(r.URL.Query())

	// Parse window (required)
	window, err := opencost.ParseWindowWithOffset(qp.Get("window", ""), env.GetParsedUTCOffset())
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'window' parameter: %s", err), http.StatusBadRequest)
		return
	}

	// Parse aggregate (optional, default: pod)
	aggregateRaw := qp.Get("aggregate", "")
	var aggregateBy []string
	if aggregateRaw != "" {
		aggregateBy = qp.GetList("aggregate", ",")
	} else {
		aggregateBy = []string{"pod"}
	}
	aggregateBy, err = ParseAggregationProperties(aggregateBy)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'aggregate' parameter: %s", err), http.StatusBadRequest)
		return
	}

	// Parse and validate filter (optional)
	filterString := qp.Get("filter", "")
	if filterString != "" {
		parser := allocationfilter.NewAllocationFilterParser()
		if _, err := parser.Parse(filterString); err != nil {
			http.Error(w, fmt.Sprintf("Invalid 'filter' parameter: %s", err), http.StatusBadRequest)
			return
		}
	}

	// Parse bufferMultiplier (optional, default: 1.2)
	bufferMultiplier := qp.GetFloat64("bufferMultiplier", 1.2)
	if bufferMultiplier <= 0 {
		http.Error(w, "Invalid 'bufferMultiplier' parameter: must be positive", http.StatusBadRequest)
		return
	}

	// Call shared efficiency computation
	metrics, err := ComputeEfficiency(a.Model, window, aggregateBy, filterString, bufferMultiplier)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error computing efficiency: %s", err), http.StatusInternalServerError)
		return
	}

	// Sort by CostSavings descending to highlight high-value optimization targets
	sort.Slice(metrics, func(i, j int) bool {
		return metrics[i].CostSavings > metrics[j].CostSavings
	})

	// CSV export
	if isCSVRequest(qp) {
		filename := buildCSVFilename("efficiency", window, aggregateRaw)

		var buf bytes.Buffer
		truncated, err := writeEfficiencyCSV(&buf, metrics, 10000)
		if err != nil {
			log.Errorf("failed to write efficiency CSV: %v", err)
		}
		if truncated {
			w.Header().Set("X-CSV-Truncated", "true")
		}

		setCSVDownloadHeaders(w, filename)
		if err := writeUTF8BOM(w); err != nil {
			log.Errorf("failed to write UTF-8 BOM: %v", err)
			return
		}
		if _, werr := w.Write(buf.Bytes()); werr != nil {
			log.Errorf("failed to write CSV response: %v", werr)
		}
		return
	}

	// JSON response
	w.Header().Set("Content-Type", "application/json")
	w.Write(WrapData(map[string]any{
		"efficiencies": metrics,
	}, nil))
}

// writeEfficiencyCSV writes efficiency metrics as CSV.
// Columns per the plan:
//
//	Name, WindowStart, WindowEnd, CPUCoresRequested, CPUCoresUsed,
//	RAMBytesRequested, RAMBytesUsed, CPUEfficiency, MemoryEfficiency,
//	RecommendedCPURequest, RecommendedRAMRequest, ResultingCPUEfficiency,
//	ResultingMemoryEfficiency, CurrentTotalCost, RecommendedCost,
//	CostSavings, CostSavingsPercent
// maxRows limits the number of data rows written (excluding header). If maxRows
// <= 0, no limit is applied. Returns true if the output was truncated.
func writeEfficiencyCSV(w io.Writer, metrics []*EfficiencyMetric, maxRows int) (bool, error) {
	fmtFloat := func(f float64) string {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}

	header := []string{
		"Name", "WindowStart", "WindowEnd",
		"CPUCoresRequested", "CPUCoresUsed",
		"RAMBytesRequested", "RAMBytesUsed",
		"CPUEfficiency", "MemoryEfficiency",
		"RecommendedCPURequest", "RecommendedRAMRequest",
		"ResultingCPUEfficiency", "ResultingMemoryEfficiency",
		"CurrentTotalCost", "RecommendedCost",
		"CostSavings", "CostSavingsPercent",
	}

	csvWriter := csv.NewWriter(w)
	if err := csvWriter.Write(header); err != nil {
		return false, fmt.Errorf("failed to write CSV header: %w", err)
	}

	rowCount := 0
	truncated := false
	for _, m := range metrics {
		if m == nil {
			continue
		}
		if maxRows > 0 && rowCount >= maxRows {
			truncated = true
			break
		}
		row := []string{
			m.Name,
			m.Start.Format("2006-01-02T15:04:05Z"),
			m.End.Format("2006-01-02T15:04:05Z"),
			fmtFloat(m.CPUCoresRequested),
			fmtFloat(m.CPUCoresUsed),
			fmtFloat(m.RAMBytesRequested),
			fmtFloat(m.RAMBytesUsed),
			fmtFloat(m.CPUEfficiency),
			fmtFloat(m.MemoryEfficiency),
			fmtFloat(m.RecommendedCPURequest),
			fmtFloat(m.RecommendedRAMRequest),
			fmtFloat(m.ResultingCPUEfficiency),
			fmtFloat(m.ResultingMemoryEfficiency),
			fmtFloat(m.CurrentTotalCost),
			fmtFloat(m.RecommendedCost),
			fmtFloat(m.CostSavings),
			fmtFloat(m.CostSavingsPercent),
		}
		if err := csvWriter.Write(row); err != nil {
			return truncated, fmt.Errorf("failed to write CSV row: %w", err)
		}
		rowCount++
	}

	csvWriter.Flush()
	return truncated, csvWriter.Error()
}
