# Performance & Scalability Report

> **Status: methodology fixed, results not yet measured.** Every number below is written
> only after the corresponding run exists in [experiments/](../experiments); the figures
> are produced by [analysis/plot.py](../analysis/plot.py) from those CSVs (`make figures`).
> Nothing here is pre-filled, estimated, or copied from a vendor benchmark.

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

## Exp1: Scale Sweep

**Hypothesis.** At a fixed submission rate, growing node count lowers scheduling
throughput and raises P99, with super-linear degradation past some fleet size.

**Setup.** IV: nodes ∈ {100, 500, 1000, 1500, 2000}. Controlled: submission QPS, pod
resource requests, client QPS/burst, default scheduler, `--optimize=false`. 5 levels × 3 runs.

**Figures.** `exp1-scheduling-p99-vs-nodes.png`, `exp1-throughput-vs-nodes.png`,
`exp1-host-load.png`, `latency-breakdown-stacked.png` (all in
[analysis/figures/](../analysis/figures)).

**Results.** _Not yet measured._

**Observations.** _TBD after the runs._

**Can conclude:** the direction and knee position of each control-plane metric versus
fleet size *in this simulated environment*.
**Cannot conclude:** absolute latencies under real etcd, networking, and kubelets; behaviour
beyond this host's capacity — anything larger is extrapolation and is labelled as such.
Any point where the host-load figure shows saturation is annotated "limited by the
simulation host, not by the control plane".

## Exp2: Gang vs Default

**Hypothesis.** Near resource saturation with multi-node PodGroups, the default scheduler
leaves partially placed groups holding GPUs; `topogang`'s all-or-nothing admission raises
the fully-ready rate and reduces long-lived partial placement.

**Setup.** IV: scheduler ∈ {default, topogang}. Controlled: 500 nodes, `minMember=8`,
1 GPU per pod, identical arrival seed per pair, initial utilization tuned so not every
group fits. 2 arms × 3 runs.

**Figures.** `exp2-podgroup-ready-rate.png`, `exp2-time-to-ready-cdf.png`.

**Results.** _Not yet measured._

**Observations.** _TBD after the runs._

**Can conclude:** the direction and magnitude of the gang semantics' effect on whole-group
availability and half-held resources in this environment.
**Cannot conclude:** any end-to-end training or inference speedup — there is no real data
plane. Score normalization and Permit timeout are confounders; both are fixed and recorded
in each run's `meta.json`.

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
