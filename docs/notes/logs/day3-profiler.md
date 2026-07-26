# Day 3 — Latency Profiler: scaffold, TODO list, and reference URLs

Scaffold is in place and `go build ./... && go vet ./... && go test ./...` is green.
Everything marked **HAND-WRITE** below is a stub that compiles and returns zero values —
that is deliberate. The tests for those stubs are written but `t.Skip`ped; unskip each
one as you implement it.

## What the scaffold gives you

| File | Status | Why |
|---|---|---|
| `pkg/profiler/quantile.go` | done (Day 3 core, already written) | `PodTimeline`, nearest-rank `Quantiles`, `CensoredRate` |
| `pkg/profiler/timeline.go` | **HAND-WRITE** `ExtractState`, `Tracker.Observe` | the four-moment extraction — the measurement decision itself |
| `pkg/profiler/join.go` | `LoadSubmitLog` done · **HAND-WRITE** `Join` | JSONL decode is boilerplate; the join + censoring rules are not |
| `pkg/profiler/report.go` | CSV writers done · **HAND-WRITE** `Summarize` | per-phase sample selection is where honesty lives |
| `pkg/profiler/watch.go` | done (vibe) | informer factory + handler + cache sync |
| `pkg/profiler/promql.go` | done (vibe) | `/api/v1/query` HTTP client + `histogram_quantile` query |
| `cmd/profiler/main.go` | done (vibe) | flags, kubeconfig, signal handling, output wiring |
| `pkg/profiler/timeline_test.go`, `join_test.go` | skipped stubs | each skipped test names one rule you must implement |

Design choice carried over from Day 2: `Tracker` operates on `PodState`, a client-go-free
projection, so the state machine is unit-testable with no cluster — same role `Submitter`
plays in `pkg/loadgen`.

## TODO list

### A. Four-moment extraction — `ExtractState` (must hand-write)
- [ ] A1. Read `PodScheduled` from `pod.Status.Conditions`; **only** `Status == ConditionTrue` counts. A `False` condition that you treat as scheduled is the bug that makes P99 look great.
- [ ] A2. Take `ScheduledAt` from the condition's `LastTransitionTime` (server clock, 1s granularity), not from your observation clock.
- [ ] A3. Take `NodeName` from `pod.Spec.NodeName` — non-empty means the binding is durable in etcd.
- [ ] A4. Read `Ready` from the `Ready` condition, not `phase == Running`.
- [ ] A5. Set `ObservedAt` from the injected clock so tests are deterministic.
- Refs: [PodStatus](https://pkg.go.dev/k8s.io/api/core/v1#PodStatus) · [PodCondition](https://pkg.go.dev/k8s.io/api/core/v1#PodCondition) · [PodConditionType](https://pkg.go.dev/k8s.io/api/core/v1#PodConditionType) · [Pod lifecycle / conditions](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-conditions)

### B. Timeline state machine — `Tracker.Observe` (must hand-write)
- [ ] B1. Key by pod **UID** (`pod.ObjectMeta.UID`), matching `loadgen.Record.UID`. Not by name: `GenerateName` means the name isn't known until after creation, and names get reused.
- [ ] B2. First-write-wins per moment. Informers re-deliver and resync; a later event must never move a recorded `ScheduledTs`/`BoundTs`/`ReadyTs`.
- [ ] B3. Decide `ScheduledTs` vs `BoundTs`. Suggested split, which makes the two clocks explicit: `ScheduledTs` = condition `LastTransitionTime` (server side), `BoundTs` = `ObservedAt` of the first event with `spec.nodeName != ""` (client side). `BoundTs - ScheduledTs` is then your measured watch propagation — reading question #1, and the thing you cross-check against Prometheus.
- [ ] B4. Out-of-order case: a pod first seen already-Ready (profiler started late, or a resync). Pick a policy — backfill from condition timestamps, or refuse the sample — and encode it in `TestTracker_ObservedAlreadyReady`.
- [ ] B5. Never set `SubmitTs` here; it only exists in the loadgen JSONL.
- Refs: [sample-controller informer + handler](https://github.com/kubernetes/sample-controller/blob/master/controller.go) · [ResourceEventHandlerFuncs](https://pkg.go.dev/k8s.io/client-go/tools/cache#ResourceEventHandlerFuncs) · [ObjectMeta.UID](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1#ObjectMeta)

### C. Submit-log join + censoring — `Join` (must hand-write)
- [ ] C1. Join on UID; fill `SubmitTs` from the log.
- [ ] C2. Submitted but never observed → keep the row, `Censored=true`, count `Unobserved`. It is not a zero-latency sample and it is not deletable.
- [ ] C3. Observed but not Ready by `cutoff` → `Censored=true`, and it *still* contributes a valid scheduling-latency sample if it got scheduled.
- [ ] C4. Observed but absent from the log (leftover pods from an earlier run) → count `Unsubmitted`, exclude from every submit-relative phase.
- [ ] C5. Never fabricate a timestamp. Zero `time.Time` means unknown; every consumer checks `.IsZero()` before subtracting.
- Refs: [SIG-Scalability SLOs — pod startup latency & watch latency](https://github.com/kubernetes/community/blob/main/sig-scalability/slos/slos.md) · [right-censoring](https://en.wikipedia.org/wiki/Censoring_(statistics))

### D. Per-phase summary — `Summarize` (must hand-write)
- [ ] D1. Build each phase's sample slice with an `.IsZero()` guard on **both** endpoints. Note: `PodTimeline.Scheduling()/Binding()/ColdStart()/EndToEnd()` in `quantile.go` subtract unconditionally, so a zero endpoint yields a garbage duration — filter before calling, or add IsZero-safe variants.
- [ ] D2. Log, don't silently drop, negative durations — a negative scheduling latency means the join is wrong.
- [ ] D3. Report per-phase `Count` alongside `Total` and `CensoredRate`. "P99 = 120 ms over 43 of 500 pods" is honest; "P99 = 120 ms" is not (reading question #3).
- Refs: `Quantiles` already handles nearest-rank; contrast it with [`histogram_quantile`](https://prometheus.io/docs/prometheus/latest/querying/functions/#histogram_quantile) (bucket interpolation) when you write the cross-check paragraph.

### E. Baseline run — Exp1 first two points
- [ ] E1. `N=100`: profiler first, then loadgen; `--scheduler-name default-scheduler`, constant 30 QPS, 120s.
- [ ] E2. `N=500`: same, after `scripts/spawn-nodes.sh`.
- [ ] E3. Record client-side scheduling P99 vs `--prom-url` server-side P99, same order of magnitude, difference explained.
- [ ] E4. Record the censored fraction for both runs.
- [ ] E5. Fill `docs/notes/logs/Day03.md` and commit `experiments/exp1-scale-sweep/{N100,N500}.csv`.
- Refs: [Prometheus instant query API](https://prometheus.io/docs/prometheus/latest/querying/api/#instant-queries) · [kwok metric/latency emulation caveats](https://kwok.sigs.k8s.io/docs/user/kwok-manage-nodes-and-pods/)

## Run

```bash
# terminal A — profiler first, always
go run ./cmd/profiler --kubeconfig ~/.kube/config \
  --submit-log experiments/_raw/exp1-N500.jsonl \
  --out experiments/exp1-scale-sweep/N500.csv \
  --duration 120s --drain 60s --prom-url http://localhost:9090

# terminal B — loadgen
go run ./cmd/loadgen --arrival constant --qps 30 --duration 120s \
  --scheduler-name default-scheduler --out experiments/_raw/exp1-N500.jsonl
```

`--out` gets the per-pod rows; a sibling `N500-summary.csv` gets the per-phase quantiles.

## Reading-question answers (fill in after the run)

1. Which field marks "scheduled", and what is the watch propagation delay you measured?
2. Client-observed quantiles vs Prometheus `histogram_quantile`: how do they differ and by how much?
3. How did you count pods still not Ready at the end?
