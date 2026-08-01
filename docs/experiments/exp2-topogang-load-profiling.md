# Exp2 — TopoGang Load Profiling Protocol

**Status: the 4–64 Pod/s controlled-load matrix is complete; larger-node and
high-occupancy extensions in this protocol remain unmeasured.** Measured results are
reported in [the main report](../REPORT.md); unexecuted extensions remain explicitly
labelled as designs.

Scope: this profiles the **gang scheduling path** — the TopoGang plugin
([pkg/scheduler/plugins/topogang/](../../pkg/scheduler/plugins/topogang/)) plus the scheduler
queue and binding machinery around it. Exp2 asks *whether* gang semantics change the
whole-group ready rate; its controlled-load phase asks *what limits the rate at which gangs can be admitted,
and which component's cost grows with scale*.

The focused B0/B1/B2 experiment that deliberately drives H1 from early exit into its
real full-scan path is specified separately in
[Exp2 controlled-load PreFilter bottleneck](exp2-controlled-prefilter-bottleneck.md).

---

## 1. The question, stated so it can be answered wrong

> As fleet size N, gang width M, and GPU contention ρ increase, which stage of the
> submit → group-ready path grows fastest, and does it grow because of **CPU work inside
> the plugin**, **lock contention on shared gang state**, **queue/barrier waiting**, or
> the **API write path**?

Those four are physically different bottlenecks with different fixes, and they are
distinguishable by measurement — that is the whole design constraint below. "The scheduler
is slow" is not an answer this experiment is allowed to produce.

### The attribution identity

Every gang's wall-clock time decomposes without residual into:

```
group_ready_time
  = max over members of (
        t_submit       first member submitted → this member submitted
      + t_queue        this member submitted → first pop from the active queue
      + t_cycle        scheduling cycle: PreFilter + Filter + Score + Reserve  (CPU + locks)
      + t_permit       Permit Wait: barrier time until the M-th member arrives  (not CPU)
      + t_bind         binding cycle: assume → API write → observed nodeName
      + t_ready        kwok Pod-stage delay (simulated, not real)
    )
  + t_requeue          Σ over failed attempts: backoff + re-entry into t_queue
```

These `t_*` names are the canonical vocabulary for the rest of this document. A
bottleneck claim must name the term that grows; descriptions such as "the scheduler is
slow" or "lock contention is high" are incomplete until they are connected to one of
these terms.

| Term | Standard meaning | Typical cause when it grows |
| --- | --- | --- |
| `t_submit` | first gang member submitted → this member submitted | loadgen's intra-gang submission spread; instrument cost, not scheduler cost |
| `t_queue` | submit → first pop from the active queue | queue depth, backoff, or scheduler admission rate |
| `t_cycle` | one scheduling attempt: PreFilter + Filter + Score + Reserve | plugin CPU work or locks; use `t_prefilter`, `t_filter`, `t_score`, and `t_reserve` when a finer split is available |
| `t_permit` | Reserve complete → Permit allows/rejects the Pod | waiting for all M gang members; semantic waiting unless it triggers retries |
| `t_bind` | Permit allowed → the profiler first observes `spec.nodeName` | scheduler binding machinery, apiserver/APF, or watch propagation |
| `t_ready` | first observed `spec.nodeName` → Ready | kwok's simulated Pod-stage delay; outside the scheduler |
| `t_requeue` | sum of failed-attempt backoff and queue re-entry delays | Permit rejection, unschedulable retries, or queue churn |
| `t_group_ready` | first member submitted → all M members Ready | the primary end-to-end gang response; equals the identity above |

Every term has a measurement path in §3. A term that is <10% of `t_group_ready` is
**not** the bottleneck no matter how steeply it scales — that Amdahl check is applied
before any optimization is proposed.

---

## 2. Pre-registered hypotheses in `t_*` form

Ranked from reading the current implementation, not from measurement. Each names the
*discriminating signal* — the observation that would confirm it and, crucially, the
observation that would kill it. The first question for every hypothesis is therefore:
**which `t_*` term grows?**

### H1 — `t_cycle ↑` through `t_prefilter ↑`: whole-cluster GPU scan

[plugin.go](../../pkg/scheduler/plugins/topogang/plugin.go) `PreFilter` calls
`SnapshotSharedLister().NodeInfos().List()` and `gangFitsGPU` walks nodes until the group
fits. Per Pod. With P pods and N nodes that is **O(P·N)** on the critical path, on top of
the framework's own per-node Filter pass.

The early exit hides it at low contention: when GPUs are plentiful the loop returns after
a handful of nodes. When the cluster is nearly full (ρ→1) *every* node must be visited
before the function can return `false` — so this term's cost is expected to be **coupled
to contention, not just to N**. A scheduler that is fast at ρ=0.5 and collapses at ρ=1.3
is the signature.

- **Confirms:** `t_prefilter` P99, measured by
  `plugin_execution_duration_seconds{plugin="TopoGang",extension_point="PreFilter"}`,
  P99 grows ~linearly in N *and* jumps sharply between ρ=0.95 and ρ=1.3; pprof CPU shows
  `gangFitsGPU` in the top frames.
- **Kills:** `t_prefilter` P99 is flat in N, or is <10% of `t_cycle`.

### H2 — `t_cycle ↑` through `t_score ↑`: global Registry lock wait

`Registry` ([podgroup.go](../../pkg/scheduler/plugins/topogang/podgroup.go)) guards *all*
groups with a single `sync.Mutex` — a deliberate choice (documented there) to avoid
lock-ordering deadlocks. But `Score` calls `PlacedInDomain` **once per candidate node**,
and `PlacedInDomain` takes that global lock and scans the group's reservations. So one Pod
scoring against N nodes acquires the process-wide gang lock N times, serializing against
every other scheduling goroutine and against Permit callbacks.

- **Confirms:** mutex profile attributes significant contention to `Registry.mu`;
  `t_score` P99 grows with N while scheduler CPU utilization stays *below*
  saturation (waiting, not working); throughput does not improve when the host has spare cores.
- **Kills:** mutex profile shows negligible delay at `Registry.mu`, or `t_score` P99 is flat.

### H3 — `t_permit ↑`: semantic barrier wait, not necessarily a bottleneck

`t_permit` is by construction the time until the M-th member arrives. For wide gangs it
will likely be the single largest term — and optimizing it is meaningless, because it is
the semantics, not overhead. This hypothesis exists to prevent the experiment from
"discovering" that a barrier waits.

The real question underneath it: does waiting cost *anything else*? Waiting pods hold
Reserve-assumed resources in the scheduler cache, which reduces the effective capacity
seen by later PreFilter calls (H1) and can drive groups into timeout-reject-requeue
cycles (H4).

- **Confirms:** `t_permit` is the largest term but `scheduling_attempt_duration_seconds`
  and throughput are unaffected by M → report as expected cost, not a bottleneck.
- **Escalates to H4:** `t_permit` large *and* reject/requeue rate rises with M.

### H4 — `t_requeue ↑` and repeated `t_queue + t_cycle`: reject/requeue churn

On permit timeout the whole group is rejected, Unreserve fires, and all M members re-enter
the queue with backoff. Every retry pays `t_queue + t_cycle` again. If groups routinely time out under
contention, the scheduler burns most of its cycles on work it throws away — the classic
gang failure mode. Under this hypothesis, adding scheduler CPU makes throughput *worse*.

- **Confirms:** `pod_scheduling_attempts` P99 > 1 (well above the default arm's), rising
  rejects, and `scheduler_pending_pods{queue="unschedulable"}` sawtoothing with the
  backoff period.
- **Kills:** attempts P99 == 1 across all cells.

### H5 — `t_bind ↑`: binding cycle / apiserver write path

Binding is asynchronous and concurrent, so it should overlap; but at high burst QPS the
POST-binding path and APF queueing can dominate. This is the Exp3 axis, included here only
so it can be ruled out rather than silently confounding.

- **Confirms:** `t_bind` share rises with submission QPS while t_cycle is flat;
  `apiserver_flowcontrol_current_inqueue_requests` non-zero.
- **Kills:** t_bind share flat and <10%.

### H6 — `t_submit ↑` or all scheduler `t_*` values are invalid: the instrument is the bottleneck

client-go throttling in loadgen, the profiler's own watch lag, or host CPU saturation from
kwok itself. This repo has already been burned once by measuring its own instrument
(Day 2 preflight, and the 1-second-granularity clock bug in Day 3) — so H6 is checked
before any other hypothesis is even considered.

- **Ruled out when:** client-side rate-limited count == 0, sustained QPS within 5% of
  target, host CPU < 70% during the run, and the run's censored-sample rate is within the
  REPORT.md threshold.

---

## 3. Instrumentation, cheapest layer first

Do not add code before exhausting L0. Each layer costs more (build work, observer effect)
and is only justified by an ambiguity the previous layer left open.

### L0 — Framework metrics already exposed on `:10259/metrics` (zero code)

| Metric | Answers |
| --- | --- |
| `scheduler_plugin_execution_duration_seconds{plugin="TopoGang",extension_point}` | Splits `t_cycle` into `t_prefilter`, `t_filter`, `t_score`, and `t_reserve`; directly separates H1 from H2. |
| `scheduler_framework_extension_point_duration_seconds{extension_point,profile}` | TopoGang's share of each `t_cycle` component vs all other plugins. |
| `scheduler_scheduling_attempt_duration_seconds` | `t_cycle` total (note: excludes `t_permit`). |
| `scheduler_pod_scheduling_duration_seconds` / `..._attempts` | End-to-end including `t_requeue`, plus H4's retry count. |
| `scheduler_pending_pods{queue=active\|backoff\|unschedulable}` | Explains growth in `t_queue` and `t_requeue`. |
| `scheduler_queue_incoming_pods_total{event}` | *Why* pods re-enter — which cluster event unblocks gangs. |
| `scheduler_permit_wait_duration_seconds` | `t_permit` as the framework sees it (H3). |
| `scheduler_goroutines`, `go_goroutines`, `process_cpu_seconds_total` | H2 vs CPU saturation; H6. |
| `apiserver_request_duration_seconds`, `..._flowcontrol_*`, `apiserver_request_total{code="429"}` | Explains growth in `t_bind` (H5). |

Scrape both schedulers (default arm on its own port) — the default-scheduler arm is the
control that says how much of any cost is *gang-specific* rather than Kubernetes-generic.

### L1 — pprof on the scheduler process (small config change)

kube-scheduler serves `/debug/pprof/*` on the secure port when `--profiling` is enabled
(on by default; confirm it is not disabled). Collect **during steady state**, not at
startup:

```bash
# 30s CPU profile — H1 (expect gangFitsGPU / snapshot List frames)
go tool pprof -http=:8081 http://<sched>/debug/pprof/profile?seconds=30

# mutex + block profiles — H2 (require a non-zero sampling rate; see L1 caveat)
go tool pprof -http=:8082 http://<sched>/debug/pprof/mutex
go tool pprof -http=:8083 http://<sched>/debug/pprof/block
```

**Caveat that must be recorded in the run's `meta.json`:** mutex and block profiling are
off by default (`runtime.SetMutexProfileFraction(0)`, `SetBlockProfileRate(0)`). Enabling
them requires a small addition in [cmd/scheduler/main.go](../../cmd/scheduler/main.go) behind
a flag, and both perturb the thing being measured. Use a modest fraction (e.g. 5–100), run
the *same* cell with and without it, and if the enabled/disabled throughput differs by
>5%, the mutex-profiled numbers are treated as qualitative ranking only, never as latency
measurements.

### L2 — Custom `topogang_*` metrics (hand-written; `pkg/metrics/` is currently empty)

Only what L0 provably cannot answer:

| Metric | Type | Why L0 can't answer it |
| --- | --- | --- |
| `topogang_prefilter_nodes_scanned` | Histogram | Distinguishes "PreFilter is slow" from "PreFilter scanned the whole fleet" — the direct H1 discriminator, and it survives ρ changes that latency alone confounds. |
| `topogang_registry_lock_wait_seconds` | Histogram | H2 without pprof's observer effect, in production-shaped terms. |
| `topogang_podgroup_wait_seconds{outcome=allowed\|rejected}` | Histogram | Framework permit metric doesn't split by outcome; H3 vs H4 hinges on that split. |
| `topogang_gang_reject_total{reason}` | Counter | H4 numerator. |
| `topogang_waiting_pods_iterated` | Histogram | `allowWaitingGroup`/`rejectWaitingGroup` walk **all** waiting pods in the process and re-parse each one's labels, once per member → O(M·W). Invisible to L0; a genuine suspect at wide M. |

### L3 — Group-level end-to-end timeline (profiler extension)

The profiler today is per-Pod ([pkg/profiler/](../../pkg/profiler/)). Gang questions are
per-*group*: a group is ready only when its slowest member is. Needs an aggregation keyed
on the `topogang.dev/pod-group` label producing, per group: submit, first-member-scheduled,
last-member-scheduled, last-member-bound, group-ready, attempt count, final outcome.

**Known clock hazard carried over from Day 3:** derive `scheduled` from the client-side
first observation of `spec.nodeName`, never from the server-side `PodScheduled`
condition's 1-second-granularity timestamp. At kwok speeds the latter makes most gang
intervals collapse to zero or go negative.

---

## 4. Blockers — which `t_*` term cannot yet be measured

Stated up front because none of the runs below are meaningful without them:

| # | Missing or invalid `t_*` measurement | Engineering blocker | Implementation status |
| --- | --- | --- | --- |
| B0 | `t_ready` and `t_group_ready` had an invalid clock basis | use the client-side first observations of `spec.nodeName` and `Ready=True`, not the second-granularity condition timestamps; the internal `t_bind` split still comes from framework metrics | **Implemented** in [pkg/profiler/](../../pkg/profiler/) |
| B1 | no gang-keyed `t_group_ready` samples could be created | loadgen must emit `topogang.dev/pod-group` / `min-member` labels and submit fixed-width groups | **Implemented** in [cmd/loadgen/main.go](../../cmd/loadgen/main.go) and [pkg/loadgen/](../../pkg/loadgen/) |
| B2 | `t_group_ready` could not be aggregated from member timelines | aggregate by group ID and report incomplete/unfinished groups as censored | **Implemented** in [pkg/profiler/group.go](../../pkg/profiler/group.go) |
| B3 | `t_prefilter`, Registry lock-wait time, and reject outcome could not be directly attributed | register the bounded-cardinality `topogang_*` metrics from L2 | **Implemented** in [pkg/scheduler/plugins/topogang/metrics.go](../../pkg/scheduler/plugins/topogang/metrics.go) |
| B4 | the lock-wait share inside `t_score` could not be confirmed by runtime profiles | make mutex/block profile rates settable | **Implemented** in [cmd/scheduler/main.go](../../cmd/scheduler/main.go) |
| B5 | the N×ρ effect on `t_prefilter`, `t_requeue`, and `t_group_ready` could not be tested | pre-fill to a target ρ and report achieved ρ from bound filler Pods | **Implemented; cluster validation pending** in [scripts/exp2-topogang-prefill.sh](../../scripts/exp2-topogang-prefill.sh) |

B0, B1, B2, and B5 were hard blockers: without them the primary `t_group_ready`
measurement was absent, invalid, or could not be tested across the planned cells. Their
code paths now exist; B5 still needs validation against a live disposable kwok cluster.
B3 and B4 were conditional blockers and are implemented but remain disabled/unused unless
L0 cannot separate the suspected components inside `t_cycle`.

---

## 5. Design

**Not a full factorial.** 3 axes × 3 levels = 27 cells × 3 repeats = 81 runs, which at
this scale is days of wall-clock for interactions nobody has hypothesized. Use
**one-factor-at-a-time around a base point**, which isolates each main effect at
7 cells / 21 runs — and add a targeted 2-cell interaction probe only if H1 survives, since
H1 explicitly predicts an N×ρ interaction.

**Base point (B):** N=500 nodes, ρ=0.95, M=8, 30 QPS submission, `permitTimeout=30s`,
scheduler=topogang, seed=42, 180 s + 60 s drain.

| Cell | N (nodes) | ρ (demand/capacity) | M (minMember) | Purpose |
| --- | --- | --- | --- | --- |
| B | 500 | 0.95 | 8 | base |
| N1 | 100 | 0.95 | 8 | H1 slope in N |
| N2 | 1000 | 0.95 | 8 | H1 slope in N |
| R1 | 500 | 0.50 | 8 | H1 early-exit effect |
| R2 | 500 | 1.30 | 8 | H1 worst case + H4 churn |
| M1 | 500 | 0.95 | 2 | H3/H5 slope in M |
| M2 | 500 | 0.95 | 32 | H3 + `waiting_pods_iterated` |
| **C** | 500 | 0.95 | 8 | **control: `default-scheduler`, identical load** — separates gang-specific cost from Kubernetes-generic cost |
| I1* | 1000 | 1.30 | 8 | conditional: N×ρ interaction, only if H1 survives B/N2/R2 |

`*` conditional cell — run only if pre-registered condition holds.

- **Independent variables:** N, ρ, M, scheduler (the C arm).
- **Controlled and recorded in every `<run>-meta.json`:** node template, GPU per pod,
  arrival model + seed, target QPS/burst, client-go QPS/burst, permitTimeout, topology
  key, scheduler config hash, git SHA, kwok version, host CPU/RAM, profiling flags,
  concurrent load on the host.
- **Repeats:** 3 per cell, reported as mean with min/max whiskers. Single-run values are
  never reported (REPORT.md rule).
- **Order:** randomize cell order across the 3 repeat rounds so thermal/host drift does
  not alias onto an axis.

### Dependent variables

**Primary:** `t_group_ready` P50/P95/P99 (nearest-rank, over n of N groups, censored count
reported), and admitted-groups-per-second.

**Secondary (the attribution):** `t_submit`, `t_queue`, `t_cycle` and its extension-point
split, `t_permit` split by outcome, `t_bind`, `t_ready`, and `t_requeue`; plus scheduling
attempts per Pod P99, reject rate, peak `pending_pods` per queue, scheduler CPU seconds per
admitted group *(the cleanest efficiency number — it is invariant to how fast the host
is)*, mutex-profile top frames, and host CPU/RSS.

---

## 6. Decision table — observation → conclusion → fix

Written before the data exists, so the reading of the data is not a choice made afterwards.

| Observation | Bottleneck | Fix to evaluate next |
| --- | --- | --- |
| `t_prefilter ↑` with N and ρ, and `nodes_scanned ≈ N` | **H1** | Cache a cluster-level free-GPU aggregate invalidated on node/pod events; or bound the scan and treat PreFilter as advisory-only (Permit already enforces correctness) |
| `t_score ↑` with N, mutex wait at `Registry.mu` is high, CPU is not saturated | **H2** | Per-group lock or sharded registry with a fixed lock order; or snapshot the domain counts once per Pod in PreFilter into CycleState instead of N locked reads |
| `t_permit` dominates, attempts P99 == 1, throughput flat in M | **H3** | Nothing. Report as the semantic cost of gang; document it |
| `t_requeue ↑`, attempts P99 > 1, rejects rise with ρ | **H4** | Backoff tuning; admit-order fairness so the same group isn't perpetually starved; consider not reserving until the group is likely to fit |
| `t_bind ↑` with QPS while `t_cycle` stays flat, APF in-queue > 0 | **H5** | Exp3 territory: batched binding, fewer redundant writes |
| `t_submit ↑`, client-throttled > 0, host CPU > 70%, or sustained QPS below target | **H6** | Invalid run. Fix the instrument, re-run, publish nothing |
| Topogang arm ≈ control arm C for every `t_*` term | none of the above | The gang plugin is not where the time goes; profile the shared scheduler path instead |

Note the last two rows: the design must be able to conclude "there is no gang-specific
bottleneck". A profiling experiment that can only find bottlenecks in the component it
suspects is not an experiment.

---

## 7. Run protocol

For each cell, for each of 3 repeats:

1. Fresh kwok cluster; spawn N nodes ([scripts/spawn-nodes.sh](../../scripts/spawn-nodes.sh));
   wait all-Ready.
2. Pre-fill GPU allocation to target ρ with a filler workload on a *different*
   schedulerName; record achieved ρ, don't assume it (B5).
3. Start the topogang scheduler (or default, for arm C). Wait for informer sync.
4. **Warm-up:** 30 s of load, discarded — Go's JIT-free but the informer caches, snapshot,
   and queue are cold, and first-attempt numbers are systematically pessimistic.
5. Start the profiler with this run's label selector; start metric scrapes (both
   schedulers + apiserver) at 1 s resolution.
6. Run load for 180 s.
7. At t=90 s (mid-steady-state), pull a 30 s CPU profile; pull mutex/block profiles only
   in the pprof-enabled variant runs.
8. Drain 60 s. Stop. Write `<run>-meta.json`, per-pod CSV, per-group CSV, metrics
   snapshots, and pprof files under `experiments/exp2-topogang/controlled-load/`.
9. Tear down the cluster completely before the next run — leftover state between runs is
   how a "scale effect" turns out to be an accumulation effect.

**Validity gate applied before a run is analyzed (H6):** client-throttled == 0; sustained
QPS within 5% of target; host CPU < 70%; censored group rate under the REPORT.md
threshold. Failing runs are re-run, not repaired.

---

## 8. Figures

- `exp2-extension-point-breakdown.png` — stacked per-extension-point P99 vs N, topogang
  vs control arm. The main result.
- `exp2-time-decomposition.png` — the §1 identity as stacked bars per cell.
- `exp2-prefilter-scan-vs-rho.png` — `nodes_scanned` distribution across ρ (H1).
- `exp2-cpu-per-admitted-group.png` — efficiency vs N and M.
- `exp2-queue-timeline.png` — `pending_pods` by queue over time (H4 sawtooth).

Per repo convention ([analysis/plot.py](../../analysis/plot.py)): a figure with no input data
is **skipped with a printed reason**, never drawn from placeholders.

---

## 9. Threats to validity

- **Simulated environment.** kwok means no kubelet, no image pull, no real GPU. Every
  latency here is control-plane + configured stage delay. Absolute numbers do not transfer
  to real hardware; slopes and component *shares* are the transferable findings.
- **Observer effect.** pprof mutex/block profiling perturbs exactly the contention it
  measures (§3 L1 caveat). Metric scrapes at 1 s add load to the scheduler being measured.
- **Single host.** Scheduler, apiserver, etcd, kwok, and loadgen share CPU. Above ~1k
  nodes this host is the binding constraint, and any point where the host-load figure
  saturates is annotated "limited by the simulation host, not by the control plane".
- **Clock granularity.** B0 now uses the first client observations of `spec.nodeName`
  and `Ready=True`. That keeps `t_ready` and `t_group_ready` at sub-second precision, but
  the internal `t_bind` split must still be read from framework/apiserver metrics rather
  than inferred from `PodScheduled.LastTransitionTime`.
- **ρ is approximate.** Achieved contention depends on where the filler landed; record
  achieved, not target.
- **M=32 with N=500 changes two things at once** — gang width *and* the number of
  concurrently waiting pods. If M2 shows an effect, a follow-up must separate them.

---

## 10. Results

_Not yet measured._

## 11. Observations

_TBD after the runs._

**Will be able to conclude:** which component of the gang scheduling path dominates
group-time-to-ready in this simulated environment, how each component scales in N, ρ, and
M, and whether the cost is gang-specific (vs the default-scheduler control arm).

**Will not be able to conclude:** absolute scheduler throughput on real clusters; any
training or inference speedup (there is no data plane); behaviour beyond this host's
capacity — anything larger is extrapolation and is labelled as such.
