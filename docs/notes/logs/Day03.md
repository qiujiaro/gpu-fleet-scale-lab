# Day 03 — Latency Profiler + First Baseline Sweep

- **Date:** 2026-07-26
- **Time spent:** ~Xh
- **Planned deliverable:** P50/P95/P99 latency breakdown; first batch of Exp1 data.
- **Core module to hand-write:** **Profiler**.

## Goals for today
- [x] Build a profiler that breaks end-to-end latency into submitted → scheduled → bound → ready.
- [x] Compute P50/P95/P99 and report the valid sample count for each phase.
- [x] Run the N=100 baseline and capture the first Exp1 dataset.
- [ ] Resolve the timestamp-precision problem and rerun N=100 before treating the quantiles as valid.
- [ ] Run the N=500 baseline.

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
  excluded negative durations as designed.
- **Current status:** The dataset and the measurement failure are preserved for
  analysis, but this run is not accepted as a valid latency baseline. The KWOK
  condition timestamp generation must retain adequate precision before rerunning.

## Open questions
- How should the KWOK stage configuration be changed so `LastTransitionTime` retains
  sub-second precision?
- After fixing timestamp precision, how close is the client-observed scheduling P99
  to the Prometheus `histogram_quantile` result?
- Why were 1,852 of 3,498 submitted Pods still censored after the drain period?
- Should the drain interval be increased after timestamp correctness is fixed?

## Commit(s) / artifacts
- `experiments/_raw/exp1-N100.jsonl`
- `experiments/exp1-scale-sweep/N100.csv`
- `experiments/exp1-scale-sweep/N100-summary.csv`

## Plan for tomorrow
- Fix or reconfigure KWOK condition timestamps to preserve sub-second precision.
- Rerun N=100 and verify that phase counts are consistent with the available
  endpoints and that negative durations are exceptional rather than dominant.
- Compare client-observed scheduling P99 with the Prometheus server-side P99.
- Run the N=500 baseline only after validating the N=100 measurement.
