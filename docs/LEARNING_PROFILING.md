# Learning Performance Profiling

## Purpose

The three catalog experiments establish reproducible performance behavior. They do
not, by themselves, teach profiling. Profiling starts after a benchmark exposes a
symptom and asks a different question:

> Which code path or wait state consumes the resource, and does changing it cause
> the observed performance problem to change?

The exercises below form a practical profiling track for this repository. They
are learning exercises, not additions to the canonical Exp1/Exp2/Exp3 matrices.
Promote one into the catalog only before collecting formal data.

## The profiling model

| Evidence | Question answered |
| --- | --- |
| Benchmark result | How fast, how much, or how often? |
| Time-series metric | When and under which load does the symptom appear? |
| CPU/heap profile | Which code retains or consumes the resource? |
| Mutex/block/goroutine profile | Where does execution wait? |
| Controlled intervention | Is the suspected mechanism causal? |

Use all five layers. A flame graph alone can identify expensive code, but it
cannot prove that the code caused lower throughput or higher latency.

## Repeatable workflow

1. Define one observable symptom, such as scheduling-cycle P95 increasing.
2. Decompose it with metrics, for example PreFilter time, bind time, and queue
   wait.
3. State a falsifiable hypothesis before opening a profile.
4. Change one pressure variable while keeping the other controls fixed.
5. Capture a profile during the steady or stressed interval, not during setup.
6. Match hot code or wait sites to the metric decomposition.
7. Apply one intervention and repeat the identical experiment.
8. Reject the hypothesis if the predicted metric and profile changes do not
   appear.

For every formal comparison, keep the scheduler build, Git commit, workload,
arrival model, client limits, cluster controls, measurement window, and repeat
policy fixed unless one is the declared variable.

## Common evidence package

Start the shared telemetry helper with every run. It produces:

- `<prefix>-meta.json`: exact Git state, declared controls, timing, host identity,
  and final status.
- `<prefix>-host.csv`: sampled scheduler and API-server process CPU/RSS.
- `<prefix>-prometheus.csv`: configured Prometheus queries.
- `<prefix>-apiserver.csv`: API latency and request-rate snapshots.
- `<prefix>-pressure.csv`: APF queue and HTTP 429 snapshots.

Host RSS is a comparative process signal, not an exact measurement of physical
memory consumption. Profiles must use the same measurement window recorded by
the telemetry artifacts.

## Exercise 1: Find a CPU bottleneck in PreFilter

**Hypothesis:** As feasible GPU capacity becomes scarce, TopoGang scans more
nodes in `PreFilter`; the scan consumes a material part of the scheduling cycle
and reduces admitted-gang throughput.

| Type | Definition |
| --- | --- |
| Variable | GPU occupancy: cells `50%`, `95%`, `99%` |
| Fixed | `N=500`, target QPS `64`, gang size `8`, scheduler build, workload template, client limits, seed, and run duration |
| Repeats | Three per cell |
| Profiles | One 30-second CPU profile during the measurement interval |
| Metrics | `topogang_prefilter_nodes_scanned`, PreFilter P95, scheduling-cycle P95, scheduler CPU, admitted gangs/s |

The hypothesis becomes credible when the stressed cells show all of the
following:

- node scans approach fleet size;
- PreFilter P95 grows materially from the 50% baseline;
- PreFilter becomes a meaningful share of the scheduling critical path;
- the CPU profile attributes cost to the TopoGang feasibility path; and
- throughput or queueing degrades while bind/client controls remain stable.

The stricter decision rules for this exercise are already specified in
[Controlled PreFilter Bottleneck Study](experiments/exp2-controlled-prefilter-bottleneck.md).

Example profile capture:

```bash
curl -ksSf \
  'https://127.0.0.1:10260/debug/pprof/profile?seconds=30' \
  -o cpu.pprof
go tool pprof -top cpu.pprof
go tool pprof -http=:8081 cpu.pprof
```

## Exercise 2: Distinguish CPU work from contention

**Hypothesis:** Larger gangs increase contention or blocking in the TopoGang
registry and Permit path, rather than merely increasing CPU instructions.

| Type | Definition |
| --- | --- |
| Variable | Gang size: cells `8`, `32`, `64` |
| Fixed | `N=500`, high GPU occupancy, target QPS `64`, scheduler build, workload shape apart from gang size, client limits, seed, and run duration |
| Repeats | Three per cell |
| Profiles | Mutex, block, and goroutine profiles during the same measurement interval |
| Metrics | `topogang_registry_lock_wait_seconds`, `topogang_waiting_pods_iterated`, Permit wait/reject outcomes, scheduler goroutines, throughput, and scheduling-cycle P95 |

Enable runtime sampling with scheduler flags such as:

```text
--topogang-mutex-profile-fraction=10
--topogang-block-profile-rate=1000000
```

Capture profiles from:

```text
/debug/pprof/mutex
/debug/pprof/block
/debug/pprof/goroutine
```

Run a profiling-disabled control cell. If enabling profiling changes throughput
or latency by more than 5%, treat the sampled profiles as qualitative evidence
and do not compare their absolute timings with the unprofiled benchmark.

## Exercise 3: Detect bottleneck migration

**Hypothesis:** Coalesced binding reduces API-server pressure, but at a low
concurrency limit it moves waiting into the scheduler-local bind queue.

| Type | Definition |
| --- | --- |
| Variable | Binder: `DefaultBinder`, then `CoalescedBind` with `maxInFlight=1`, `8`, `32` |
| Fixed | Same 500-Pod burst, node state, Pod template, scheduler build except the binder configuration, client limits, seed, and measurement window |
| Repeats | Three per cell |
| Profiles | Goroutine and block profiles around the burst peak |
| Metrics | API request P99, APF queue peak, HTTP 429s, bind latency, local bind-queue wait, goroutine state, and end-to-end scheduling P99 |

Do not call the change an optimization merely because API pressure falls. It is
an improvement only if end-to-end behavior also improves and the new local wait
does not violate the latency objective.

This exercise connects the profiling evidence to the canonical Exp3 comparison
in [the experiment catalog](experiments/CATALOG.md).

## Exercise 4: Find retained state or leaked goroutines

**Hypothesis:** Repeated gang creation and cleanup leaves registry entries,
waiting Pods, queued bind work, or goroutines behind.

| Type | Definition |
| --- | --- |
| Variable | Burst number: ten identical small bursts |
| Fixed | Burst size, gang size, node state, arrival rate, scheduler build, cleanup delay, and sampling interval |
| Repeats | Repeat the full ten-burst sequence at least three times |
| Profiles | Heap and goroutine profiles after warm-up and after the final cleanup |
| Metrics | `go_memstats_heap_objects`, `go_goroutines`, registry size, waiting Pods, queued bind work, and post-cleanup RSS |

Allow a fixed cleanup and garbage-collection observation window after each burst.
The important signal is a rising post-cleanup floor across bursts, not a
temporary allocation peak. Inspect retained allocation paths and recurring
goroutine stacks before labeling the behavior a leak.

Capture from:

```text
/debug/pprof/heap
/debug/pprof/goroutine
```

## Before-and-after optimization rule

An optimization is not validated by a cleaner flame graph. Repeat the same
experimental cell with only the implementation changed and compare:

- the user-visible outcome: throughput, queueing, or end-to-end latency;
- the decomposed metric associated with the hypothesis;
- the relevant profile hot path or wait site;
- CPU, memory, API pressure, and goroutine side effects; and
- run-to-run variance across at least three repeats.

Record the baseline and candidate Git SHAs separately. Never mix profile files
or telemetry outputs from different builds in one run directory.

## Minimum learning deliverables

For each exercise, write:

1. the hypothesis and its rejection condition;
2. the fixed controls and the single variable;
3. one profile screenshot or `pprof -top` excerpt;
4. the matching metric plot;
5. a code-path explanation from symptom to mechanism;
6. the intervention and identical before/after comparison; and
7. what the evidence cannot prove.

You have learned the core profiling workflow when you can explain not only
where time or memory went, but also why that mechanism changed an end-to-end
result and which controlled evidence would disprove your explanation.

## Repository references

- [Experiment catalog](experiments/CATALOG.md)
- [Exp2 profiling notes](experiments/exp2-topogang-load-profiling.md)
- [Controlled PreFilter study](experiments/exp2-controlled-prefilter-bottleneck.md)
- [Scheduler profiling flags](../cmd/scheduler/main.go)
- [TopoGang profiling metrics](../pkg/scheduler/plugins/topogang/metrics.go)
- [Shared telemetry command](../cmd/telemetry/main.go)
