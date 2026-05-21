# Efficiency Calculation Formulas

## Purpose

This document records the current efficiency formulas used by the allocation-related APIs and the dedicated `/efficiency` API.

There are currently two different efficiency concepts in the codebase:

1. Allocation efficiency
2. Recommendation efficiency

They are not interchangeable and should not be described with the same business meaning.

## 1. Allocation Efficiency

This is the efficiency value shown on allocation-derived results such as:

- `/allocation`
- `/allocation/compute`
- allocation summary/topline style pages derived from allocation data

Relevant code:

- [core/pkg/opencost/summaryallocation.go](/Users/zhangpeng/GolandProjects/github.com/Gentleelephant/opencost/core/pkg/opencost/summaryallocation.go:268)
- [core/pkg/opencost/summaryallocation.go](/Users/zhangpeng/GolandProjects/github.com/Gentleelephant/opencost/core/pkg/opencost/summaryallocation.go:430)
- [core/pkg/opencost/summaryallocation.go](/Users/zhangpeng/GolandProjects/github.com/Gentleelephant/opencost/core/pkg/opencost/summaryallocation.go:457)

### Per-record CPU efficiency

```text
cpuEfficiency =
  if cpuCoreRequestAverage > 0:
    cpuCoreUsageAverage / cpuCoreRequestAverage
  else if cpuCoreUsageAverage == 0 or cpuCost == 0:
    0
  else:
    1
```

### Per-record RAM efficiency

```text
ramEfficiency =
  if ramByteRequestAverage > 0:
    ramByteUsageAverage / ramByteRequestAverage
  else if ramByteUsageAverage == 0 or ramCost == 0:
    0
  else:
    1
```

### Per-record total efficiency

```text
totalEfficiency =
  (cpuCost * cpuEfficiency + ramCost * ramEfficiency) / (cpuCost + ramCost)
```

### Important behavior

- Only `CPU` and `RAM` participate in `totalEfficiency`
- `PV`, `GPU`, `Network`, `LoadBalancer`, `SharedCost`, and `ExternalCost` do not participate in this formula
- `cpuEfficiency` and `ramEfficiency` can be greater than `1`
- Idle rows such as `__idle__` return `0` efficiency in raw data and are usually rendered as `-` in the UI

## 2. Aggregated Allocation Efficiency

When a total row is shown across multiple allocation records or multiple time buckets, the total is not the arithmetic average of row efficiencies.

The system first aggregates resource request/usage and cost, then recalculates efficiency.

Relevant code:

- [core/pkg/opencost/summaryallocation.go](/Users/zhangpeng/GolandProjects/github.com/Gentleelephant/opencost/core/pkg/opencost/summaryallocation.go:1399)
- [core/pkg/opencost/summaryallocation.go](/Users/zhangpeng/GolandProjects/github.com/Gentleelephant/opencost/core/pkg/opencost/summaryallocation.go:1431)
- [core/pkg/opencost/summaryallocation.go](/Users/zhangpeng/GolandProjects/github.com/Gentleelephant/opencost/core/pkg/opencost/summaryallocation.go:1463)

### Aggregated CPU efficiency

```text
aggregatedCpuEfficiency =
  sum(cpuCoreUsageAverage_i * minutes_i) / sum(cpuCoreRequestAverage_i * minutes_i)
```

Idle rows are excluded.

### Aggregated RAM efficiency

```text
aggregatedRamEfficiency =
  sum(ramByteUsageAverage_i * minutes_i) / sum(ramByteRequestAverage_i * minutes_i)
```

Idle rows are excluded.

### Aggregated total efficiency

```text
aggregatedTotalEfficiency =
  (
    aggregatedCpuEfficiency * sum(cpuCost_i)
    + aggregatedRamEfficiency * sum(ramCost_i)
  ) / (
    sum(cpuCost_i) + sum(ramCost_i)
  )
```

Idle rows are excluded from the aggregated cost and request/usage inputs.

## 3. Example Matching Current UI

For the example where:

- `host` efficiency is about `32.2%`
- `17-4` efficiency is `0%`
- totals efficiency is about `27.4%`

the total row is computed by:

```text
1. Aggregate non-idle CPU request and usage across host + 17-4
2. Aggregate non-idle RAM request and usage across host + 17-4
3. Sum non-idle CPU costs across host + 17-4
4. Sum non-idle RAM costs across host + 17-4
5. Recalculate total efficiency from those aggregate values
```

It is not computed by:

- averaging `32.2%` and `0%`
- dividing by total displayed cost

Because the displayed total cost includes `PV`, while allocation efficiency does not.

## 4. Dedicated `/efficiency` API

The `/efficiency` API uses a different algorithm intended for rightsizing and recommendation output.

Relevant code:

- [pkg/costmodel/efficiency.go](/Users/zhangpeng/GolandProjects/github.com/Gentleelephant/opencost/pkg/costmodel/efficiency.go:62)

### Per-resource efficiency

```text
cpuEfficiency = cpuCoresUsed / cpuCoresRequested
memoryEfficiency = ramBytesUsed / ramBytesRequested
```

Where:

```text
cpuCoresUsed = CPUCoreHours / hours
ramBytesUsed = RAMByteHours / hours
cpuCoresRequested = CPUCoreRequestAverage
ramBytesRequested = RAMBytesRequestAverage
```

### Recommendation calculation

```text
recommendedCPURequest = max(cpuCoresUsed * bufferMultiplier, minimumCPU)
recommendedRAMRequest = max(ramBytesUsed * bufferMultiplier, minimumRAM)
```

Then the API estimates resulting efficiency and potential savings by comparing current request-based cost with recommended request-based cost.

This endpoint is designed for optimization recommendations, not for reproducing allocation-page efficiency percentages.

## 5. Recommendation

The `/efficiency` API should not be changed to use the allocation efficiency formula unless the product meaning of the endpoint is also changed.

Reason:

- Allocation efficiency answers: "How efficiently are requested CPU/RAM resources being used in the current allocation view?"
- `/efficiency` answers: "What would recommended requests and savings look like after applying a buffer to observed usage?"

If both are forced into one formula, the endpoint will lose its recommendation semantics.

Recommended product direction:

1. Keep `/efficiency` as a recommendation API
2. Keep allocation-derived efficiency as a reporting metric
3. In UI and API docs, explicitly distinguish:
   - `allocation efficiency`
   - `recommendation efficiency`

If the current UI is expected to show the same efficiency number everywhere, then the safer change is not to rewrite `/efficiency`, but to add a separate field or endpoint that exposes allocation-style efficiency explicitly.
