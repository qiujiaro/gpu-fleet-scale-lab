# Performance & Scalability Report

> **Status: Exp1 and Exp2 complete; Exp3 is a planned follow-up.** Every reported number
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

- **Repeats.** Every configuration runs ≥3 times; figures show the mean with min/max
  whiskers. Single-run values are never reported.
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

## Exp1: Simulated GPU Fleet Scale Sweep

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

Source: [`experiments/smoke-scale/smoke-scale-results.csv`](../experiments/smoke-scale/smoke-scale-results.csv).

**Can conclude:** this environment reached a 2,000-node simulated fleet and observed
the readiness times above. **Cannot conclude:** real kubelet, GPU, CNI, storage, or
hardware initialization performance, or scheduler throughput versus fleet size.

## Exp2: TopoGang Behavior and Controlled-Load Sweep

**Questions.** Does TopoGang prevent partial placement when a group cannot fit, and
which part of its scheduling path becomes limiting as offered load rises?

**Behavior result.** In the live insufficient-capacity preview, the default scheduler
placed 3/4 members while TopoGang placed 0/4, leaving the incomplete group pending
instead of partially occupying GPUs.

**Controlled-load setup.** TopoGang at 4, 8, 16, 32, and 64 Pod/s; three repeats per
level; 200 four-member gangs and 800 Pods per run; 20 simulated nodes with 64 GPUs per
node. One infrastructure-invalid run submitted no Pods and is excluded.

**Results.** All 14 valid runs completed 800/800 Pods with zero censoring and zero gang
rejection. Gang P95 measured after the final member was submitted stayed between
15.98 and 31.04 ms. Registry lock wait stayed around or below 1 µs on average, and no
tested QPS crossed the saturation rule.

The raw Pod scheduling P95 fell from 766.68 ms at 4 QPS to 55.65 ms at 64 QPS because
the four members arrived closer together. With sequential submission, the expected
intra-gang span is `3/QPS`; the falling curve is Permit barrier semantics, not evidence
that extra load accelerates the scheduler.

See [the experiment design](exp2p-gang-bottleneck-profiling.md), the
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
scripts/spawn-nodes.sh 1000                 # fake GPU nodes with topology labels
go run ./cmd/profiler --submit-log experiments/_raw/exp1-N1000-run1.jsonl \
  --out experiments/exp1-scale-sweep/N1000-run1.csv --duration 180s &
go run ./cmd/loadgen --arrival poisson --qps 50 --duration 180s \
  --out experiments/_raw/exp1-N1000-run1.jsonl
make figures                                # analysis/plot.py -> analysis/figures/*.png
```

Data paths: raw submit logs in [experiments/_raw/](../experiments/_raw); per-run timeline,
summary, and `meta.json` under [experiments/exp1-scale-sweep/](../experiments/exp1-scale-sweep),
[exp2-gang/](../experiments/exp2-gang), [exp3-coldstart/](../experiments/exp3-coldstart).
`analysis/plot.py` skips any figure whose input CSV is absent and prints why, so a partial
data set produces a partial report rather than an invented one.
