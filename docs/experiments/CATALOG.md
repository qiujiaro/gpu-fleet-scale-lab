# Experiment Catalog

All results apply only to a laptop-hosted KWOK control-plane simulation. Nodes, kubelets,
GPU capacity, and cold-start timing are simulated. The experiments do not measure real
GPUs, image pulls, container runtimes, CNI, storage, or NVLink/InfiniBand performance.

## Status

| ID | Guide-aligned experiment | Current repository evidence | Status against this contract |
| --- | --- | --- | --- |
| Exp0 | Load Generator and Client Calibration | 50 Pod/s client preflight | Complete prerequisite |
| Exp1 | Control-Plane Scale Sweep | Fleet-readiness timing while a warm fleet grew from 1,000 to 2,000 simulated nodes | **Partial** — the scheduling scale sweep is missing |
| Exp2 | Gang Scheduling vs Default under Resource Contention | Two-scheduler isolation, one insufficient-capacity behavior preview, and a TopoGang-only 4–64 Pod/s load matrix | **Partial** — the paired scheduler comparison is missing |
| Exp3 | Burst Scale-Out and API Server Pressure | Simulated cold-start configuration, baseline/optimized scheduler profiles, coalesced binder, and a one-arm runner | **Scaffold implemented; live validation and formal measurements missing** |

Exp0 is a prerequisite calibration rather than one of the guide's three main experiments.
Its artifacts remain under `experiments/exp0-loadgen/`.


## Exp1 — Control-Plane Scale Sweep

### Question and hypothesis

At a fixed Pod submission rate, how does increasing the simulated fleet size affect
scheduling latency, scheduling throughput, and API server latency?

The guide hypothesis is that scheduler throughput falls and API server P99 rises as the
node count grows, with possible super-linear degradation after a scale knee. This is a
hypothesis, not a result.

**Variable**

| Variable | Cells | Repeats |
| --- | --- | --- |
| Ready nodes `N` | `100, 500, 1000, 1500, 2000` | 3 per cell; 15 formal runs total |

**Fixed**

| Control | Value |
| --- | --- |
| Cluster | Fresh KWOK cluster; repository node template; all `N` nodes Ready, then 60s settle |
| Scheduler | `default-scheduler`; no TopoGang or CoalescedBind |
| Workload | Exactly 3,000 non-gang `pause:3.9` Pods; 1 simulated GPU/Pod; no cold-start label |
| Arrival | Constant 50 Pod/s; token burst 50; 50 workers; `seed=42` |
| Client | client-go 200 QPS / 400 burst |
| Timing | Profiler starts first; 90s submission limit; 60s drain |
| Telemetry | Prometheus every 5s; host CPU/memory/disk every 1s |
| Environment | Same versions, configs, PromQL, host power mode, and background load; clean namespace and queues between runs |

Implementation prerequisite: add independent `--max-pods`, `--client-qps`, and
`--client-burst` flags.

**Measurements**

| Metric | Definition |
| --- | --- |
| Scheduling latency | First observed `spec.nodeName` minus successful Create response; P50/P95/P99/max, sample count, censored count |
| Scheduling throughput | 3,000 scheduled Pods divided by first-submit to last-scheduled span |
| Client validity | Achieved QPS, attempts, successes, failures, retries, limiter wait, 429s |
| API server | Pod-Create P50/P95/P99, request count, 429 delta, APF queue/rejects |
| Scheduler | Scheduling-attempt P50/P95/P99; active/backoff/unschedulable queue peaks |
| Environment | Ready nodes before/after; host CPU, memory, disk/IO |

### Required outputs and decision boundary

- Node count versus scheduling P99, with min/max or confidence/error bars and the chosen
  SLO reference line.
- Node count versus scheduled Pods/s.
- Host CPU/memory versus node count so a laptop limit is not mislabeled as a Kubernetes
  control-plane limit.

The result may identify trends and a knee **in this simulation on this host**. It may not
claim real-kubelet, network, etcd-at-production-scale, GPU, or hyperscale absolute
performance.

### Current evidence and remaining work

`experiments/exp1-fleet-readiness/results.csv` measures how quickly incremental fake
nodes became Ready on an already-warm cluster. It is useful environment/preflight
evidence, but it does not submit the controlled Pod workload or measure any primary
outcome above. Exp1 still needs the 15-run matrix, per-run metadata, Prometheus/host
metrics, and the three required figures.

---

## Exp2 — Gang Scheduling vs Default under Resource Contention

### Question and hypothesis

When GPU capacity is nearly full and complete PodGroups cannot all fit, does TopoGang's
all-or-nothing admission improve complete-group readiness and reduce long-lived partial
placement relative to the default scheduler?

The guide hypothesis is that the default scheduler can bind part of a group while the
remaining members wait, whereas TopoGang trades possible rejection/waiting for fewer
half-placed groups. It does not hypothesize real training or inference acceleration.

**Variable**

| Variable | Cells | Repeats |
| --- | --- | --- |
| `scheduler` | `default-scheduler`, `topogang` | 3 paired repeats using seeds `42, 43, 44`; 6 formal runs; paired arms alternate order |

**Fixed**

| Control | Value |
| --- | --- |
| Cluster | Fresh 500-node fleet; 64 GPUs/node; eight NVLink domains; three zones |
| Occupancy | One 63-GPU filler Pod/node; 31,500/32,000 GPUs occupied; 500 GPUs free (`98.4375%`) |
| Workload | 100 PodGroups × 8 members; 1 GPU/member; 800-GPU total demand; no cold-start label |
| Arrival | Poisson 40 Pod/s; burst 80; 80 workers; identical paired trace/member order |
| Client | client-go 200 QPS / 400 burst |
| TopoGang | `permitTimeout=30s`; NVLink topology key; `nvidia.com/gpu`; Score weight 1; other default plugins unchanged |
| Timing | Profiler starts first; cutoff 60s after final member submission; filler capacity never released |
| Telemetry | Scheduler/API server every 5s; host CPU/memory every 1s |

Invalid pair: different occupancy, trace, seed, topology, timeout, or cutoff.

**Measurements**

| Metric | Definition |
| --- | --- |
| Complete groups | Groups with eight Ready members / 100 submitted groups |
| Partial groups | Groups with 1–7 scheduled members at cutoff; held Pods/GPUs |
| Gang latency | First-submit→last-Ready and last-submit→last-Ready; P50/P95/P99 and CDF for complete groups; incomplete groups counted separately |
| TopoGang | Permit allow/reject counts, reasons, wait P50/P95/P99 |
| Scheduler | Attempts/Pod, queue peaks, scheduling-cycle and Bind P50/P95/P99 |
| API/client validity | Achieved QPS, failures/retries/throttling, API P99, APF peak, 429s |
| Environment | Simulated GPU allocation, Ready nodes, host CPU/memory |

### Required outputs and decision boundary

- Default versus TopoGang complete-group readiness and partial-placement bars.
- PodGroup time-to-ready CDF, with incomplete/censored groups shown separately.
- Reject/wait and utilization table so better readiness is not claimed by hiding rejected
  groups or unused capacity.

The result may quantify gang admission's effect on whole-group availability and partial
resource occupation in this controlled simulation. It may not claim real GPU utilization,
fabric locality, or application speedup.

### Current evidence and remaining work

The repository already proves scheduler ownership isolation and contains a live
insufficient-capacity preview (`default=3/4` placed, `topogang=0/4` placed). It also has
a valid TopoGang-only 4–64 Pod/s matrix. Those answer useful implementation and
throughput questions, but they are not the paired, same-trace, near-saturation comparison
specified here.

Exp2 still needs three paired repeats, controlled/recorded occupancy, complete-group and
partial-placement summaries, and the comparison figures. The focused PreFilter
high-occupancy extension remains a separate follow-up design in
[exp2-controlled-prefilter-bottleneck.md](exp2-controlled-prefilter-bottleneck.md).

---

## Exp3 — Burst Scale-Out and API Server Pressure

### Question and hypothesis

During an inference-style Pod burst, can bounded/coalesced binding smooth API server and
APF pressure without materially worsening end-to-end simulated cold-start latency?

The original guide describes the optimization as reducing redundant API writes. The
implemented `CoalescedBind` plugin cannot do that: Kubernetes still requires one
successful `/binding` operation per bound Pod. The repository-valid hypothesis is
therefore about limiting concurrent writes and smoothing pressure, not reducing the
number of required writes.

**Variable**

| Variable | Cells | Repeats |
| --- | --- | --- |
| Bind implementation | `DefaultBinder` (`day6-baseline`), `CoalescedBind` (`day6-optimized`) | 3 paired repeats; 6 formal runs; alternate arm order |

**Fixed**

| Control | Value |
| --- | --- |
| Cluster | Fresh 1,000-node fleet; all Ready, then 60s settle; no filler Pods |
| Workload | Exactly 500 non-gang `pause:3.9` Pods; 1 GPU/Pod; cold-start label enabled |
| Arrival | No pre-burst load; release all 500 Creates at `t=30s`; 500 workers; burst 500 |
| Client/scheduler | loadgen client 1,000 QPS / 1,000 burst; scheduler 200 QPS / 400 burst |
| Simulated cold start | KWOK duration 1,000ms + jitter 2,000ms (simulated 1–3s) |
| CoalescedBind | `window=5ms`, `maxBatch=16`, `maxInFlight=8`, `queueCapacity=256` |
| Timing | Observe to `t=120s` or all Ready plus 10s; unfinished Pods remain censored |
| Telemetry | Prometheus and host CPU/memory every 1s; identical PromQL/range |
| Environment | Same nodes, Pod order, versions, APF config, non-Bind plugins, and host load |

Implementation prerequisite: support an exact zero-preload 500-Pod burst and independent
client QPS/burst settings.

**Measurements**

| Metric | Definition |
| --- | --- |
| Burst validity | First-to-last Create span ≤5s; achieved rate, attempts, retries, failures, limiter wait, 429s |
| Pod latency | submit→scheduled, scheduled→Ready, submit→Ready P50/P95/P99/max and censoring |
| API server | One-second P99 time series; Pod Create/Binding counts and latency; APF queue peak/rejects; 429 delta |
| Scheduler | Bind P50/P95/P99, queue peaks, scheduling-attempt latency, CPU, goroutines |
| CoalescedBind | Queue depth/wait, batch sizes, in-flight Bind peak, backpressure, errors |
| Completion/environment | Ready/500; host CPU/memory peak |

Successful `/binding` requests must equal successfully bound Pods in both arms. Fewer
writes is a validity failure, not an optimization result.

### Required outputs and decision boundary

- Baseline versus optimized API server P99 time series.
- Comparison bars/table for APF queue peak, 429 count, Bind latency, and end-to-end P99.
- Request-count invariant and scheduler-side metrics, to expose a bottleneck merely moved
  from the API server into the scheduler queue.

The result may quantify pressure smoothing and its latency trade-off in this KWOK
control-plane setup. It may not claim fewer required binding writes, real image-pull or
GPU cold-start improvement, registry throttling behavior, or confidential-container
performance.

### Current evidence and remaining work

The cold-start Stage, both scheduler profiles, the CoalescedBind implementation/tests,
and `scripts/exp3-binding-arm.sh` exist. No `experiments/exp3-burst/` result matrix is
checked in, the runner currently emits a constant stream rather than the contracted
500-Pod burst at `t=30s`, and it snapshots scheduler metrics but does not yet collect the
required API server/APF range time series or write `meta.json`.

Exp3 still needs a paired matrix orchestrator, the exact burst workload, three paired
seeds/repeats, API server/APF and host telemetry collection, run validity summaries, raw
data, figures, and report conclusions.

## Canonical data layout

```text
experiments/
  exp0-loadgen/
    client-preflight/
  exp1-scale-sweep/
    N100/<run-id>/
    N500/<run-id>/
    N1000/<run-id>/
    N1500/<run-id>/
    N2000/<run-id>/
  exp2-gang/
    default/<run-id>/
    topogang/<run-id>/
  exp3-burst/
    baseline/<run-id>/
    optimized/<run-id>/
```

Existing `experiments/exp1-fleet-readiness/` and `experiments/exp2-topogang/` artifacts
are retained as provenance for the narrower work already completed. Existing run IDs
beginning with `exp2p-` are also retained because run IDs are provenance, not current
experiment names.

## Rules shared by Exp1–Exp3

- Run every cell at least three times. A single run may be a smoke test, never a
  comparative result.
- Write immutable raw artifacts plus a `meta.json` for every run. At minimum record the
  Git SHA, host and component versions, scheduler profile, node count, workload shape,
  arrival model, target and achieved QPS, client QPS/burst, seed, timeouts, simulated
  cold-start parameters, and Prometheus scrape interval.
- Randomize or interleave arm order across repeat rounds. Paired arms use the same seed.
- Record attempted, successful, failed, and rate-limited submissions; observed and
  censored Pods/PodGroups; Ready-node count; scheduler health; and host CPU/memory.
- Report P50/P95/P99 with sample count and censoring beside every distribution. Generate
  figures only from checked-in raw data; never copy expected values into result files.
- Reject or clearly label a run when the client misses target QPS by more than 5%, there
  are unexpected submission failures, required nodes are not Ready, scheduler health
  fails, censoring invalidates the tail, or host saturation dominates the cell.
- Keep raw data under `experiments/`; put interpretation in `docs/REPORT.md`.

---