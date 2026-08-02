# Performance & Scalability Report

> **Status: Exp0 and Exp1 are complete; Exp2 is complete within the tested 4–64 Pod/s range;
> Exp3 is a planned follow-up.** Every reported number
> is backed by a run artifact under [experiments/](../experiments). Nothing is
> pre-filled, estimated, or copied from a vendor benchmark.

## Environment

Fill in from the machine that produced the data in `experiments/` — not from the machine
you happen to be reading this on.

| Item | Value |
| --- | --- |
| Host (CPU / RAM / OS) | _TBD_ |
| kwok / kwokctl version | _TBD_ |
| Kubernetes (kwokctl control plane) version | _TBD_ |
| Go version | _TBD_ |
| Prometheus scrape interval | _TBD_ |
| Date measured | _TBD_ |

**Simulation boundary.** kwok simulates the control plane only — apiserver, scheduler,
controllers, and watch timing. There are no kubelets, container runtimes, image pulls,
GPUs, or networking. "Cold start" is a configured kwok Pod stage delay, so every
cold-start number measures *the simulated delay plus control-plane propagation*, never a
real image pull.

## Methodology

- **Repeats.** The protocol targets ≥3 attempts per configuration; figures report the
  retained successful-repeat count and show mean with min/max whiskers. Single-run values
  are never promoted as comparative results.
- **Control variables.** Each run writes `<run>-meta.json` (nodes, QPS, scheduler,
  `optimize`, seed, durations, client QPS/burst). `plot.py` reads control variables from
  that file; a filename-derived fallback prints a warning.
- **Quantile definition.** Nearest-rank on the sorted sample
  ([pkg/profiler/quantile.go](../pkg/profiler/quantile.go)) — no interpolation, no
  histogram bucketing. Client-side quantiles are cross-checked against the server-side
  `histogram_quantile` on `apiserver_request_duration_seconds` where both exist; the two
  measure different things and are reported separately, never averaged.
- **Phase split.** `submit → scheduled` (scheduling), `scheduled → bound` (binding, i.e.
  measured watch propagation), `bound → ready` (simulated cold start), `submit → ready`
  (end-to-end). `scheduled` uses the `PodScheduled` condition's server-side
  `LastTransitionTime`; `bound` uses the client-side observation of `spec.nodeName`, so
  the binding phase mixes two clocks — stated explicitly wherever it is used.
- **Censored samples.** A pod that never reaches a phase endpoint before the watch cutoff
  is *excluded from that phase* and counted in `censored`. Every quantile is reported as
  "P99 = X ms over n of N pods"; a censored rate above a few percent invalidates the tail
  and the run is rerun with a longer drain rather than reported.
- **Stacked breakdown.** The breakdown figure stacks medians, not P99s — quantiles are
  not additive, and a stacked P99 would show a total no pod experienced.

## Exp0: Load Generator and Client Calibration

**Question.** Can the client sustain a 50 Pod/s target without failed submissions,
HTTP 429 responses, or hidden client-side throttling?

**Result.** Against a 1,000-node cluster, the client attempted and successfully submitted
2,860 Pods in 60 seconds: 47.7 Pod/s sustained, zero failures, and zero rate-limited
requests.

Source: [`experiments/exp0-loadgen/client-preflight/summary.csv`](../experiments/exp0-loadgen/client-preflight/summary.csv).

**Can conclude:** the client was not the limiting component at the calibrated target in
this environment. **Cannot conclude:** API-server or scheduler capacity above that load.

## Exp1: Fleet Readiness Scaling

**Question.** Can a laptop-hosted KWOK control plane bring a simulated GPU fleet to
2,000 Ready nodes, and how does readiness time change during the final scale steps?

**Setup.** Starting from 1,000 nodes, add 100, 400, and 500 nodes. Every node advertises
64 CPU, 512 GiB memory, 8 `nvidia.com/gpu`, and rack/zone topology labels. Nodes and
kubelets are simulated; the Kubernetes control plane is real.

**Results.**

| Nodes added | Fleet size after | Time to all-Ready |
| ---: | ---: | ---: |
| 100 | 1,100 | <1 s |
| 400 | 1,500 | 1 s |
| 500 | 2,000 | 3 s |

Source: [`experiments/exp1-fleet-readiness/results.csv`](../experiments/exp1-fleet-readiness/results.csv).

**Can conclude:** this environment reached a 2,000-node simulated fleet and observed
the readiness times above. **Cannot conclude:** real kubelet, GPU, CNI, storage, or
hardware initialization performance, or scheduler throughput versus fleet size.

## Exp2: TopoGang Scheduler, Behavior, and Load Profiling

**Questions.** Can the out-of-tree scheduler run beside the default scheduler without
cross-scheduling Pods, does TopoGang prevent partial placement when a group cannot fit,
and which part of its scheduling path becomes limiting as offered load rises?

**Scheduler-isolation result.** The Day 4 phase submitted workloads to
`default-scheduler` and `topogang` concurrently and verified that both schedulers bound
their own Pods without claiming the other profile's Pods. Artifacts are under
[`experiments/exp2-topogang/two-scheduler-isolation/`](../experiments/exp2-topogang/two-scheduler-isolation/).

**Behavior result.** In the live insufficient-capacity preview, the default scheduler
placed 3/4 members while TopoGang placed 0/4, leaving the incomplete group pending
instead of partially occupying GPUs.

**Controlled-load setup.** TopoGang at 4, 8, 16, 32, and 64 Pod/s; two retained repeats
at 4 Pod/s and three at every other level; 200 four-member gangs and 800 Pods per run;
20 simulated nodes with 64 GPUs per node.

**Results.** All 14 retained runs completed 800/800 Pods with zero censoring and zero gang
rejection. Gang P95 measured after the final member was submitted stayed between
15.98 and 31.04 ms. Registry lock wait stayed around or below 1 µs on average, and no
tested QPS crossed the saturation rule.

The raw Pod scheduling P95 fell from 766.68 ms at 4 QPS to 55.65 ms at 64 QPS because
the four members arrived closer together. With sequential submission, the expected
intra-gang span is `3/QPS`; the falling curve is Permit barrier semantics, not evidence
that extra load accelerates the scheduler.

See [the experiment design](experiments/exp2-topogang-load-profiling.md), the
[engineering/result log](notes/logs/Day05.md), and the four checked-in figures under
[`docs/notes/assets/day05`](notes/assets/day05).

**Can conclude:** TopoGang's all-or-nothing behavior was exercised live, the completed
QPS matrix found no saturation knee through 64 Pod/s, and the intentional arrival span
dominated raw scheduling latency. **Cannot conclude:** production GPU-cluster
throughput, performance at larger node counts or high occupancy, or any training-speed
improvement.

## Exp3: Burst & API Server Pressure

**Hypothesis.** A burst scale-out spikes apiserver request latency and APF queueing;
removing redundant API writes lowers apiserver P99 and 429s at equal load without
materially worsening end-to-end cold-start P99.

**Setup.** IV: `--optimize` ∈ {false, true}. Controlled: 1000 nodes, one 500-pod burst at
t=30s, cold-start delay distribution, client QPS/burst, scheduler. 2 arms × 3 runs.
Metrics: `apiserver_request_duration_seconds` P99 over time,
`apiserver_flowcontrol_current_inqueue_requests` peak,
`apiserver_request_total{code="429"}`, end-to-end P99.

**Figures.** `exp3-apiserver-p99-timeseries.png`, `exp3-pressure-bars.png`.

**Results.** _Not yet measured._

**Observations.** _TBD after the runs._

**Can conclude:** the direction and magnitude of the optimization's effect on API-server
pressure here. Scheduler-side metrics are reported alongside, because an optimization that
merely moves the bottleneck from apiserver to scheduler must be visible as such.
**Cannot conclude:** behaviour under real image-registry throttling or confidential-computing
overhead.

## Cross-cutting Findings

_TBD — which layer (apiserver, scheduler, watch propagation, simulated cold start) dominates
at each fleet size, from the breakdown figure._

## Threats to Validity

- Single-machine simulation: host CPU/IO saturation can masquerade as control-plane
  degradation; the host-load figure exists to catch this and the affected points are labelled.
- kwok abstracts the entire data plane; cold start is a configured delay.
- Results depend on the arrival seed; the seed is fixed per comparison pair and recorded.
- Binding latency mixes a server clock (1s condition granularity) with a client clock, so
  small binding values are at the resolution floor.
- kwok's own documented single-machine ceiling (~1k nodes / ~100k pods) bounds what the
  largest points mean.

## Reproducibility

```bash
kwokctl create cluster --name gpu-scale --runtime docker --prometheus-port 9090
scripts/spawn-nodes.sh 1000
make exp0-loadgen-calibration
make exp1-fleet-readiness
make exp2-two-schedulers
make exp2-topogang-preview
make exp2-topogang-load-test
make figures
```

Data paths follow the canonical [experiment catalog](experiments/CATALOG.md): Exp0 artifacts
are under [exp0-loadgen/](../experiments/exp0-loadgen), Exp1 results are under
[exp1-fleet-readiness/](../experiments/exp1-fleet-readiness), and Exp2 artifacts are
under [exp2-topogang/](../experiments/exp2-topogang).
`analysis/plot.py` skips any figure whose input CSV is absent and prints why, so a partial
data set produces a partial report rather than an invented one.
