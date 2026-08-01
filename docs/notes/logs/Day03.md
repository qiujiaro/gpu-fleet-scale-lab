# Day 03 — Latency Profiler + First Baseline Sweep

- **Date:** 2026-07-26
- **Time spent:** ~Xh
- **Planned deliverable at the time:** P50/P95/P99 latency breakdown; this was later
  reclassified as a rejected diagnostic rather than Exp1 data.
- **Core module to hand-write:** **Profiler**.
- **Outcome:** profiler complete and running; the N=100 run produced a dataset but **not
  a valid baseline** — see "Blockers".

## Goals for today
- [x] Build a profiler that breaks end-to-end latency into submitted → scheduled → bound → ready.
- [x] Compute P50/P95/P99 and report the valid sample count for each phase.
- [x] Run the N=100 baseline; later reject and reclassify it under diagnostics.
- [ ] Resolve the timestamp-precision problem and rerun N=100 before treating the quantiles as valid.
- [ ] Run the N=500 baseline.

## What the scaffold gave me

`go build ./... && go vet ./... && go test ./...` was green from the start; everything
marked **HAND-WRITE** was a stub that compiled and returned zero values, with its tests
written but `t.Skip`ped.

| File | Scaffold status | Now | Why it was mine to write |
|---|---|---|---|
| `pkg/profiler/quantile.go` | done (Day 3 core, pre-written) | unchanged | `PodTimeline`, nearest-rank `Quantiles`, `CensoredRate` |
| `pkg/profiler/timeline.go` | **HAND-WRITE** `ExtractState`, `Tracker.Observe` | written | the four-moment extraction — the measurement decision itself |
| `pkg/profiler/join.go` | `LoadSubmitLog` done · **HAND-WRITE** `Join` | written | JSONL decode is boilerplate; the join + censoring rules are not |
| `pkg/profiler/report.go` | CSV writers done · **HAND-WRITE** `Summarize` | written | per-phase sample selection is where honesty lives |
| `pkg/profiler/watch.go` | done (vibe) | unchanged | informer factory + handler + cache sync |
| `pkg/profiler/promql.go` | done (vibe) | unused this run | `/api/v1/query` client + `histogram_quantile` query |
| `cmd/profiler/main.go` | done (vibe) | unchanged | flags, kubeconfig, signal handling, output wiring |
| `timeline_test.go`, `join_test.go` | skipped stubs | **still skipped** | each names one rule; see the debt note below |

Design choice carried over from Day 2: `Tracker` operates on `PodState`, a client-go-free
projection, so the state machine is unit-testable with no cluster — same role `Submitter`
plays in `pkg/loadgen`.

**Outstanding debt:** all ten unit-test stubs are still `t.Skip`ped even though the four
functions they cover are implemented. The negative-duration disaster below is exactly the
class of bug `TestExtractState_Scheduled` (timestamp source) would have caught before a
120-second cluster run. Unskip them before the rerun.

## TODO list — final state

### A. Four-moment extraction — `ExtractState` (hand-written)
- [x] A1. Read `PodScheduled` from `pod.Status.Conditions`; **only** `Status == ConditionTrue` counts. A `False` condition treated as scheduled is the bug that makes P99 look great.
- [x] A2. Take `ScheduledAt` from the condition's `LastTransitionTime` (server clock, 1s granularity), not from the observation clock. — *This is the decision that produced today's blocker: correct in principle, and the 1s granularity turned out to be the dominant error term.*
- [x] A3. Take `NodeName` from `pod.Spec.NodeName` — non-empty means the binding is durable in etcd.
- [x] A4. Read `Ready` from the `Ready` condition, not `phase == Running`.
- [x] A5. Set `ObservedAt` from the injected clock so tests are deterministic.
- Refs: [PodStatus](https://pkg.go.dev/k8s.io/api/core/v1#PodStatus) · [PodCondition](https://pkg.go.dev/k8s.io/api/core/v1#PodCondition) · [PodConditionType](https://pkg.go.dev/k8s.io/api/core/v1#PodConditionType) · [Pod lifecycle / conditions](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-conditions)

### B. Timeline state machine — `Tracker.Observe` (hand-written)
- [x] B1. Key by pod **UID** (`pod.ObjectMeta.UID`), matching `loadgen.Record.UID`. Not by name: `GenerateName` means the name isn't known until after creation, and names get reused.
- [x] B2. First-write-wins per moment. Informers re-deliver and resync; a later event must never move a recorded `ScheduledTs`/`BoundTs`/`ReadyTs`.
- [x] B3. `ScheduledTs` = condition `LastTransitionTime` (server side); `BoundTs` = `ObservedAt` of the first event with `spec.nodeName != ""` (client side). `BoundTs - ScheduledTs` is the measured watch propagation — reading question #1.
- [ ] B4. Out-of-order case: a pod first seen already-Ready. The current code backfills from condition timestamps, but the policy was never written down or asserted — `TestTracker_ObservedAlreadyReady` is still skipped.
- [x] B5. Never set `SubmitTs` here; it only exists in the loadgen JSONL.
- Refs: [sample-controller informer + handler](https://github.com/kubernetes/sample-controller/blob/master/controller.go) · [ResourceEventHandlerFuncs](https://pkg.go.dev/k8s.io/client-go/tools/cache#ResourceEventHandlerFuncs) · [ObjectMeta.UID](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1#ObjectMeta)

### C. Submit-log join + censoring — `Join` (hand-written)
- [x] C1. Join on UID; fill `SubmitTs` from the log.
- [x] C2. Submitted but never observed → keep the row, `Censored=true`, count `Unobserved`. Not a zero-latency sample, not deletable.
- [x] C3. Observed but not Ready by `cutoff` → `Censored=true`, and it still contributes a valid scheduling-latency sample if it got scheduled.
- [x] C4. Observed but absent from the log (leftovers from an earlier run) → counted as `Unsubmitted` and never emitted, so it cannot enter a submit-relative phase.
- [x] C5. Never fabricate a timestamp. Zero `time.Time` means unknown; every consumer checks `.IsZero()` before subtracting.
- Refs: [SIG-Scalability SLOs — pod startup & watch latency](https://github.com/kubernetes/community/blob/main/sig-scalability/slos/slos.md) · [right-censoring](https://en.wikipedia.org/wiki/Censoring_(statistics))

### D. Per-phase summary — `Summarize` (hand-written)
- [x] D1. Both endpoints guarded with `.IsZero()` before subtracting, since `Scheduling()/Binding()/ColdStart()/EndToEnd()` in `quantile.go` subtract unconditionally.
- [x] D2. Negative durations logged, then dropped — never clamped, never `abs()`. **This is what surfaced the blocker rather than hiding it**, and it is the single decision that saved the day's data from being quietly wrong.
- [x] D3. Per-phase `Count` reported alongside `Total` and `CensoredRate` (reading question #3).
- Refs: `Quantiles` uses nearest-rank; contrast with [`histogram_quantile`](https://prometheus.io/docs/prometheus/latest/querying/functions/#histogram_quantile) (bucket interpolation) in the cross-check paragraph.

### E. Rejected Pod-latency baseline (formerly drafted as Exp1)
- [x] E1. `N=100`: profiler first, then loadgen; `--scheduler-name default-scheduler`, constant 30 QPS, 120s.
- [ ] E2. `N=500`: blocked on the N=100 rerun — running it now would just produce a second invalid dataset.
- [ ] E3. Client-side scheduling P99 vs `--prom-url` server-side P99. Not attempted: with 3 valid samples there is nothing to compare.
- [x] E4. Censored fraction recorded for the run that happened (52.94%).
- [x] E5. This note + `experiments/diagnostics/rejected-pod-latency-baseline/N100{,-summary}.csv` committed.
- Refs: [Prometheus instant query API](https://prometheus.io/docs/prometheus/latest/querying/api/#instant-queries) · [kwok metric/latency emulation caveats](https://kwok.sigs.k8s.io/docs/user/kwok-manage-nodes-and-pods/)

## Run

```bash
# terminal A — profiler first, always
go run ./cmd/profiler --kubeconfig ~/.kube/config \
  --submit-log experiments/diagnostics/exp1-N500.jsonl \
  --out experiments/diagnostics/rejected-pod-latency-baseline/N500.csv \
  --duration 120s --drain 60s --prom-url http://localhost:9090

# terminal B — loadgen
go run ./cmd/loadgen --arrival constant --qps 30 --duration 120s \
  --scheduler-name default-scheduler --out experiments/diagnostics/exp1-N500.jsonl
```

`--out` gets the per-pod rows; a sibling `N500-summary.csv` gets the per-phase quantiles.

## What I did
- Started the profiler before the load generator so the informer could observe Pod
  events from the beginning of the run.
- Ran the N=100 experiment with a constant arrival rate of 30 QPS for 120 seconds,
  using `default-scheduler`.
- Joined load-generator submission records with informer-observed Pod timelines by
  Pod UID.
- Preserved submitted-but-unobserved and not-ready Pods as censored samples instead
  of treating them as zero-latency samples or silently dropping them.
- Wrote 3,498 per-Pod timelines and a per-phase summary.

## What I learned
- A quantile is not meaningful without its valid sample count. Although the raw
  timelines contained endpoints for 1,646 completed Pods, most scheduling,
  cold-start, and end-to-end durations were negative and were correctly excluded
  from the quantile inputs.
- The KWOK-generated `PodScheduled` and `Ready` condition transition timestamps in
  this run had one-second precision, while the load-generator submission clock and
  profiler observation clock had sub-second precision.
- Combining those clocks directly can produce timestamps such as:
  `SubmitTs=08:52:25.946406`, `ScheduledTs=08:52:25.000000`,
  `BoundTs=08:52:25.991018`, and `ReadyTs=08:52:25.000000`.
  This yields negative scheduling and cold-start durations even though the real
  lifecycle ordering was valid.
- Negative durations must not be clamped to zero or converted to absolute values.
  Doing either would manufacture artificially good latency results.
- The reported binding distribution is also not a reliable binding/watch-latency
  measurement in this run. It is dominated by the offset between an integer-second
  condition timestamp and a sub-second client observation timestamp.
- The two-clock split from B3 was the right design and is also the thing that broke:
  mixing a server-side second-granularity timestamp with a client-side microsecond one
  inside a single subtraction is only valid when the true duration is much larger than
  the coarser clock's quantum. At kwok speeds it is not.

## Key results / numbers
- Configuration: N=100 nodes, constant 30 QPS, 120-second load, 60-second drain,
  `default-scheduler`.
- Successful submission records: 3,498.
- Per-Pod CSV rows: 3,498 data rows plus one header row.
- Complete by cutoff: 1,646.
- Censored by cutoff: 1,852 (52.94%).

| Phase | Valid count | Total | P50 | P95 | P99 | Interpretation |
|---|---:|---:|---:|---:|---:|---|
| Scheduling | 3 | 3,498 | 4.272 ms | 30.230 ms | 30.230 ms | Not statistically valid; almost all endpoint pairs produced negative durations. |
| Binding | 1,646 | 3,498 | 495.085 ms | 955.228 ms | 993.979 ms | Numerically available, but dominated by mixed timestamp precision. |
| Cold start | 0 | 3,498 | 0 ms | 0 ms | 0 ms | No valid samples; the displayed zeros mean an empty quantile set, not zero latency. |
| End-to-end | 4 | 3,498 | 1.207 ms | 30.230 ms | 30.230 ms | Not statistically valid; almost all endpoint pairs produced negative durations. |

The scheduling and end-to-end P99 values above must not be presented as baseline
performance results because they are based on only three and four valid samples,
respectively.

## Blockers & how I solved them
- **Blocker:** Most derived durations were negative.
- **Diagnosis:** `ScheduledTs` and `ReadyTs` were truncated to whole seconds, whereas
  `SubmitTs` and `BoundTs` retained sub-second precision. The profiler logged and
  excluded negative durations as designed (D2).
- **Current status:** The dataset and the measurement failure are preserved for
  analysis, but this run is not accepted as a valid latency baseline. The KWOK
  condition timestamp generation must retain adequate precision before rerunning.

## Reading-question answers

1. **Which field marks "scheduled", and what watch propagation delay did I measure?**
   The `PodScheduled` condition with `Status == True`, timestamped by
   `LastTransitionTime` (server clock). Propagation was defined as `BoundTs - ScheduledTs`
   — client observation of `spec.nodeName` minus the server condition timestamp. **The
   measurement is not usable this run:** the 495 ms P50 / 994 ms P99 "binding" figures are
   an artifact of subtracting a second-truncated server timestamp from a microsecond
   client timestamp, so they mostly encode the position within the wall-clock second
   rather than any propagation delay. The near-1000 ms P99 is the giveaway.
2. **Client-observed quantiles vs Prometheus `histogram_quantile`?** Not answered — the
   cross-check needs a valid client-side number first. The methodological difference is
   already understood and stands: `Quantiles` is nearest-rank over the exact sample set,
   `histogram_quantile` interpolates within pre-defined buckets, so they should agree in
   order of magnitude but not exactly, with the bucket edges bounding Prometheus's
   resolution. Carry this to the rerun.
3. **How did I count pods still not Ready at the end?** By right-censoring in `Join`:
   observed but no `ReadyTs` before `cutoff`, or `ReadyTs` after it, sets `Censored=true`
   and the row is kept. Submitted-but-never-observed rows are kept too, counted as
   `Unobserved`. 1,852 of 3,498 (52.94%) were censored. Every phase reports its own
   `Count` next to `Total`, so no quantile can be read without its denominator.

## Open questions
- How should the KWOK stage configuration be changed so `LastTransitionTime` retains
  sub-second precision?
- If KWOK cannot emit sub-second condition timestamps at all, is the fallback to measure
  every phase on the client clock (`ObservedAt` deltas) and accept that watch propagation
  becomes unmeasurable — or to keep both and report them as separate, non-subtractable
  series?
- After fixing timestamp precision, how close is the client-observed scheduling P99
  to the Prometheus `histogram_quantile` result?
- Why were 1,852 of 3,498 submitted Pods still censored after the drain period?
- Should the drain interval be increased after timestamp correctness is fixed?

## Commit(s) / artifacts
- `experiments/diagnostics/rejected-pod-latency-baseline/submit.jsonl`
- `experiments/diagnostics/rejected-pod-latency-baseline/N100.csv`
- `experiments/diagnostics/rejected-pod-latency-baseline/N100-summary.csv`

## Plan for tomorrow
- Unskip the ten profiler unit tests; settle the B4 already-Ready policy while doing it.
- Fix or reconfigure KWOK condition timestamps to preserve sub-second precision.
- Rerun N=100 and verify that phase counts are consistent with the available
  endpoints and that negative durations are exceptional rather than dominant.
- Compare client-observed scheduling P99 with the Prometheus server-side P99.
- Run the N=500 baseline only after validating the N=100 measurement.
