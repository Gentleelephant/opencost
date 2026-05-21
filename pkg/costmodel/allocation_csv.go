package costmodel

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/opencost/opencost/core/pkg/opencost"
)

// writeAllocationComputeCSV writes an AllocationSetRange as CSV to the writer.
// Columns follow the existing csv_export.go baseline with additional SharedCost,
// ExternalCost, and LoadBalancerCost columns.
// maxRows limits the number of data rows written (excluding header). If maxRows
// <= 0, no limit is applied. Returns true if the output was truncated.
func writeAllocationComputeCSV(w io.Writer, asr *opencost.AllocationSetRange, maxRows int) (bool, error) {
	fmtFloat := func(f float64) string {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}

	type rowData struct {
		start time.Time
		end   time.Time
		alloc *opencost.Allocation
	}

	type columnDef struct {
		column string
		value  func(data rowData) string
	}

	csvDef := []columnDef{
		{column: "Date", value: func(d rowData) string { return d.start.Format("2006-01-02") }},
		{column: "Namespace", value: func(d rowData) string {
			if d.alloc.Properties != nil {
				return d.alloc.Properties.Namespace
			}
			return ""
		}},
		{column: "Cluster", value: func(d rowData) string {
			if d.alloc.Properties != nil {
				return d.alloc.Properties.Cluster
			}
			return ""
		}},
		{column: "ControllerKind", value: func(d rowData) string {
			if d.alloc.Properties != nil {
				return d.alloc.Properties.ControllerKind
			}
			return ""
		}},
		{column: "ControllerName", value: func(d rowData) string {
			if d.alloc.Properties != nil {
				return d.alloc.Properties.Controller
			}
			return ""
		}},
		{column: "Pod", value: func(d rowData) string {
			if d.alloc.Properties != nil {
				return d.alloc.Properties.Pod
			}
			return ""
		}},
		{column: "Container", value: func(d rowData) string {
			if d.alloc.Properties != nil {
				return d.alloc.Properties.Container
			}
			return ""
		}},
		{column: "CPUCoreUsageAverage", value: func(d rowData) string { return fmtFloat(d.alloc.CPUCoreUsageAverage) }},
		{column: "CPUCoreRequestAverage", value: func(d rowData) string { return fmtFloat(d.alloc.CPUCoreRequestAverage) }},
		{column: "RAMBytesUsageAverage", value: func(d rowData) string { return fmtFloat(d.alloc.RAMBytesUsageAverage) }},
		{column: "RAMBytesRequestAverage", value: func(d rowData) string { return fmtFloat(d.alloc.RAMBytesRequestAverage) }},
		{column: "NetworkReceiveBytes", value: func(d rowData) string { return fmtFloat(d.alloc.NetworkReceiveBytes) }},
		{column: "NetworkTransferBytes", value: func(d rowData) string { return fmtFloat(d.alloc.NetworkTransferBytes) }},
		{column: "GPUs", value: func(d rowData) string { return fmtFloat(d.alloc.GPUs()) }},
		{column: "PVBytes", value: func(d rowData) string { return fmtFloat(d.alloc.PVBytes()) }},
		{column: "CPUCost", value: func(d rowData) string { return fmtFloat(d.alloc.CPUTotalCost()) }},
		{column: "RAMCost", value: func(d rowData) string { return fmtFloat(d.alloc.RAMTotalCost()) }},
		{column: "NetworkCost", value: func(d rowData) string { return fmtFloat(d.alloc.NetworkTotalCost()) }},
		{column: "PVCost", value: func(d rowData) string { return fmtFloat(d.alloc.PVTotalCost()) }},
		{column: "GPUCost", value: func(d rowData) string { return fmtFloat(d.alloc.GPUTotalCost()) }},
		{column: "TotalCost", value: func(d rowData) string { return fmtFloat(d.alloc.TotalCost()) }},
		// Additional columns not present in csv_export.go baseline
		{column: "LoadBalancerCost", value: func(d rowData) string { return fmtFloat(d.alloc.LoadBalancerCost) }},
		{column: "SharedCost", value: func(d rowData) string { return fmtFloat(d.alloc.SharedCost) }},
		{column: "ExternalCost", value: func(d rowData) string { return fmtFloat(d.alloc.ExternalCost) }},
	}

	header := make([]string, len(csvDef))
	for i, def := range csvDef {
		header[i] = def.column
	}

	csvWriter := csv.NewWriter(w)
	if err := csvWriter.Write(header); err != nil {
		return false, fmt.Errorf("failed to write CSV header: %w", err)
	}

	rowCount := 0
	truncated := false
	for _, as := range asr.Allocations {
		if as == nil {
			continue
		}
		// Sort allocation keys for stable output ordering.
		keys := make([]string, 0, len(as.Allocations))
		for k := range as.Allocations {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if maxRows > 0 && rowCount >= maxRows {
				truncated = true
				break
			}
			alloc := as.Allocations[k]
			if alloc == nil {
				continue
			}
			row := make([]string, len(csvDef))
			rd := rowData{start: as.Start(), end: as.End(), alloc: alloc}
			for i, def := range csvDef {
				row[i] = def.value(rd)
			}
			if err := csvWriter.Write(row); err != nil {
				return truncated, fmt.Errorf("failed to write CSV row: %w", err)
			}
			rowCount++
		}
		if truncated {
			break
		}
	}

	csvWriter.Flush()
	return truncated, csvWriter.Error()
}

// writeAllocationSummaryCSV writes a SummaryAllocationSetRange as CSV to the
// writer.
// maxRows limits the number of data rows written (excluding header). If maxRows
// <= 0, no limit is applied. Returns true if the output was truncated.
func writeAllocationSummaryCSV(w io.Writer, sasr *opencost.SummaryAllocationSetRange, maxRows int) (bool, error) {
	fmtFloat := func(f float64) string {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}

	type rowData struct {
		start time.Time
		end   time.Time
		sa    *opencost.SummaryAllocation
	}

	type columnDef struct {
		column string
		value  func(data rowData) string
	}

	csvDef := []columnDef{
		{column: "Name", value: func(d rowData) string { return d.sa.Name }},
		{column: "Namespace", value: func(d rowData) string {
			if d.sa.Properties != nil {
				return d.sa.Properties.Namespace
			}
			return ""
		}},
		{column: "Cluster", value: func(d rowData) string {
			if d.sa.Properties != nil {
				return d.sa.Properties.Cluster
			}
			return ""
		}},
		{column: "ControllerKind", value: func(d rowData) string {
			if d.sa.Properties != nil {
				return d.sa.Properties.ControllerKind
			}
			return ""
		}},
		{column: "ControllerName", value: func(d rowData) string {
			if d.sa.Properties != nil {
				return d.sa.Properties.Controller
			}
			return ""
		}},
		{column: "WindowStart", value: func(d rowData) string { return d.start.Format(time.RFC3339) }},
		{column: "WindowEnd", value: func(d rowData) string { return d.end.Format(time.RFC3339) }},
		{column: "CPUCoreRequestAverage", value: func(d rowData) string { return fmtFloat(d.sa.CPUCoreRequestAverage) }},
		{column: "CPUCoreUsageAverage", value: func(d rowData) string { return fmtFloat(d.sa.CPUCoreUsageAverage) }},
		{column: "RAMBytesRequestAverage", value: func(d rowData) string { return fmtFloat(d.sa.RAMBytesRequestAverage) }},
		{column: "RAMBytesUsageAverage", value: func(d rowData) string { return fmtFloat(d.sa.RAMBytesUsageAverage) }},
		{column: "CPUCost", value: func(d rowData) string { return fmtFloat(d.sa.CPUCost) }},
		{column: "RAMCost", value: func(d rowData) string { return fmtFloat(d.sa.RAMCost) }},
		{column: "GPUCost", value: func(d rowData) string { return fmtFloat(d.sa.GPUCost) }},
		{column: "NetworkCost", value: func(d rowData) string { return fmtFloat(d.sa.NetworkCost) }},
		{column: "PVCost", value: func(d rowData) string { return fmtFloat(d.sa.PVCost) }},
		{column: "LoadBalancerCost", value: func(d rowData) string { return fmtFloat(d.sa.LoadBalancerCost) }},
		{column: "SharedCost", value: func(d rowData) string { return fmtFloat(d.sa.SharedCost) }},
		{column: "ExternalCost", value: func(d rowData) string { return fmtFloat(d.sa.ExternalCost) }},
		{column: "TotalCost", value: func(d rowData) string { return fmtFloat(d.sa.TotalCost()) }},
	}

	header := make([]string, len(csvDef))
	for i, def := range csvDef {
		header[i] = def.column
	}

	csvWriter := csv.NewWriter(w)
	if err := csvWriter.Write(header); err != nil {
		return false, fmt.Errorf("failed to write CSV header: %w", err)
	}

	rowCount := 0
	truncated := false
	for _, sas := range sasr.SummaryAllocationSets {
		if sas == nil {
			continue
		}
		// Sort summary allocation keys for stable output ordering.
		keys := make([]string, 0, len(sas.SummaryAllocations))
		for k := range sas.SummaryAllocations {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if maxRows > 0 && rowCount >= maxRows {
				truncated = true
				break
			}
			sa := sas.SummaryAllocations[k]
			if sa == nil {
				continue
			}
			row := make([]string, len(csvDef))
			start, end := windowStartEnd(sas.Window)
			rd := rowData{start: start, end: end, sa: sa}
			for i, def := range csvDef {
				row[i] = def.value(rd)
			}
			if err := csvWriter.Write(row); err != nil {
				return truncated, fmt.Errorf("failed to write CSV row: %w", err)
			}
			rowCount++
		}
		if truncated {
			break
		}
	}

	csvWriter.Flush()
	return truncated, csvWriter.Error()
}

// windowStartEnd safely extracts start and end times from a Window, returning
// zero time for nil pointers.
func windowStartEnd(w opencost.Window) (time.Time, time.Time) {
	var start, end time.Time
	if s := w.Start(); s != nil {
		start = *s
	}
	if e := w.End(); e != nil {
		end = *e
	}
	return start, end
}
