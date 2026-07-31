# Exp2-P Controlled Bottleneck — Forcing the PreFilter Full-Cluster Scan

**Status: experiment design only. No result in this document is measured.**

This experiment deliberately creates the input condition that exposes a real worst-case
path in the current TopoGang implementation. It does not add `sleep`, fake latency, or
fabricated samples. The controlled variable is cluster GPU occupancy: when free GPU is
scarce and distributed across the fleet, TopoGang's whole-group capacity check must
inspect many nodes before it can accept or reject a Gang.

The broader profiling protocol and canonical `t_*` vocabulary are defined in
[Exp2-P gang bottleneck profiling](exp2p-gang-bottleneck-profiling.md). This document is
the focused experiment for its H1 hypothesis.

---

## 1. Question

> Can high GPU occupancy force TopoGang PreFilter from an early-exit path into a
> full-cluster scan, and does that make `t_prefilter` a material part of
> `t_cycle` and reduce admitted-Gang throughput?

The current PreFilter calls `NodeInfos().List()` and accumulates free GPU node by node
until the remaining Gang fits:

```text
Pod enters PreFilter
    ↓
inspect Node 1 free GPU
    ↓ insufficient
inspect Node 2
    ↓ insufficient
...
enough aggregate free GPU → Success
or all N nodes inspected → Unschedulable
```

With plentiful GPU, the loop exits after one or a few nodes. Near saturation, it can
inspect the whole fleet for every scheduling attempt. For `P` Pods and `N` nodes, the
worst-case work is approximately `O(P·N)`.

---

## 2. Why the Previous QPS Sweep Did Not Expose It

The first QPS sweep used:

```text
20 nodes
64 GPU/node
Gang size = 4
GPU/Pod = 1
```

One node could satisfy an entire four-Pod Gang, so:

```text
topogang_prefilter_nodes_scanned mean = 1
```

Increasing QPS only changed how quickly members reached the Permit barrier. It did not
change the number of nodes inspected by PreFilter. That sweep validly established that
TopoGang sustained 64 Pod/s in the low-scan configuration, but it did not test H1.

---

## 3. Causal Model in `t_*` Terms

The intended causal chain is:

```text
GPU occupancy ρ ↑
    ↓
free GPU becomes scarce/distributed
    ↓
prefilter_nodes_scanned ↑ toward N
    ↓
t_prefilter ↑
    ↓
t_cycle ↑
    ↓
scheduler service rate falls
    ↓
t_queue and t_after_submit ↑
    ↓
admitted Gang/s stops tracking offered QPS
```

Canonical measurements:

| Signal | Meaning in this experiment |
| --- | --- |
| `t_submit` | Loadgen's intra-Gang submission span; a controlled confound, not the target |
| `t_queue` | Time waiting for a scheduling cycle; should grow after service rate saturates |
| `t_prefilter` | Time in TopoGang PreFilter; the proposed growing term |
| `t_cycle` | Total scheduling attempt; should grow if PreFilter becomes material |
| `t_permit` | Semantic wait for Gang members; remove it with burst submission or `t_after_submit` |
| `t_bind` | API Server binding path; must remain flat to rule out H5 |
| `t_after_submit` | `t_group_ready - t_submit`, calculated per Gang before quantiles |
| `t_requeue` | Retry/backoff cost; separates expensive scanning from timeout churn |

The bottleneck is not confirmed merely because `nodes_scanned` is high. `t_prefilter`
must also grow and become a material share of `t_cycle` or `t_group_ready`.

---

## 4. Controlled Matrix

Use one factor at a time around a fixed 500-node cluster:

| Cell | Nodes `N` | GPU/node | Target occupancy `ρ` | Gang size `M` | Target QPS | Purpose |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| B0 | 500 | 8 | 0.50 | 8 | 64 | Early-exit baseline |
| B1 | 500 | 8 | 0.95 | 8 | 64 | High-occupancy transition |
| B2 | 500 | 8 | 0.99 | 32 | Worst-case scan probe |

Run each cell three times. Each run submits a fixed number of complete Gangs. Start with:

```text
200 Gang/run
3 repeats/cell
```

If B2 confirms the scan effect, run the scale validation:

| Cell | Nodes `N` | GPU/node | `ρ` | `M` | Target QPS |
| --- | ---: | ---: | ---: | ---: | ---: |
| N1 | 100 | 8 | 0.99 | 32 | 64 |
| N2 | 500 | 8 | 0.99 | 32 | 64 |
| N3 | 1000 | 8 | 0.99 | 32 | 64 |

The scale validation is conditional. Do not spend time on N1/N2/N3 if B2 does not first
show that `prefilter_nodes_scanned` and `t_prefilter` increase.

### Important capacity constraint

At `N=500`, 8 GPU/node gives 4000 total GPU. `ρ=0.99` leaves only about 40 GPU free.
A 32-member Gang needs 32 GPU, so small differences in achieved occupancy determine
whether the first Gang fits. The test must record **achieved**, not requested, occupancy.

The capacity edge is intentional, but B2 must not silently become a trivial
"no resources exist" test. Accept B2 only when:

```text
at least one complete Gang is admitted
and
PreFilter scans a substantial fraction of N
```

If no Gang can fit, lower achieved occupancy in small steps (for example 0.99 → 0.985 →
0.98) until both conditions hold.

---

## 5. Variables

### Independent variables

- GPU occupancy `ρ` in B0/B1/B2.
- Node count `N` in the conditional N1/N2/N3 validation.

### Held constant

- KWOK and Kubernetes versions.
- Host machine and concurrent host workload.
- 8 allocatable GPU per simulated node.
- One GPU request per test Pod.
- TopoGang scheduler configuration and binary.
- Gang size within each comparison arm.
- Loadgen arrival model, seed, target QPS, and total Gangs.
- Profiler duration/drain policy.
- Topology labels and domain distribution.
- Permit timeout.

B2 changes Gang size as an explicit worst-case probe, so B0/B1 establish the occupancy
effect at fixed `M=8`; B2 is not used by itself to estimate the causal effect of `ρ`.

### Recorded covariates

- Requested and achieved occupancy.
- Total allocatable, filler-requested, and remaining GPU.
- Ready node count.
- Git SHA and scheduler config hash.
- Actual successful Pod submission rate and client throttling.
- Scheduler CPU and host CPU during the steady-state interval when available.

---

## 6. Setup Protocol

Each cell starts from a fresh disposable KWOK cluster or a demonstrably clean equivalent.

1. Create the requested number of nodes from a dedicated 8-GPU node template.
2. Wait until all nodes are Ready.
3. Verify total allocatable GPU:

   ```text
   total_allocatable_gpu = N × 8
   ```

4. Use `scripts/prefill-gpu.sh` to create filler Pods for the target `ρ`.
5. Count bound filler Pods and calculate achieved occupancy:

   ```text
   achieved_ρ = filler_requested_gpu / total_allocatable_gpu
   ```

6. Start TopoGang scheduler and require:

   ```text
   GET https://127.0.0.1:10260/healthz → 200
   GET https://127.0.0.1:10260/metrics → 200
   ```

7. Save scheduler metrics immediately before the test.
8. Start profiler and wait for the `profiler: watching` log line.
9. Run loadgen for a fixed number of complete Gangs.
10. Drain observations, save metrics after the test, and write CSV artifacts.
11. Delete test and filler Pods.
12. Wait for scheduler queues and resource accounting to return to baseline.

Randomize cell order across repeat rounds:

```text
round 1: B0, B2, B1
round 2: B1, B0, B2
round 3: B2, B1, B0
```

This prevents thermal or host-load drift from being mistaken for an occupancy effect.

---

## 7. Required Measurements

### Validity

Every run reports:

- attempted, succeeded, failed, and rate-limited submissions;
- submitted, observed, unobserved, and censored Pods;
- complete and censored Gangs;
- achieved occupancy;
- Ready nodes before and after;
- scheduler health before and after.

### Primary outcomes

- admitted complete Gangs per second;
- `t_after_submit` P50/P95/P99;
- `t_cycle` P50/P95/P99;
- `t_prefilter` P50/P95/P99.

### Attribution signals

- `topogang_prefilter_nodes_scanned` count, mean, and P95;
- TopoGang PreFilter execution duration;
- scheduling attempt duration;
- active/backoff/unschedulable queue depth;
- scheduling attempts per Pod;
- `topogang_registry_lock_wait_seconds`;
- `topogang_podgroup_wait_seconds{outcome}`;
- `topogang_gang_reject_total{reason}`;
- Bind extension-point latency;
- scheduler CPU seconds per admitted Gang.

Collect a 30-second CPU profile during steady state for B0 and B2. B2 confirms H1 only
if `gangFitsGPU` or its snapshot/list path becomes a top CPU contributor.

---

## 8. Validity Gates

A run is excluded from performance comparison when any of these holds:

- scheduler health check fails;
- fewer nodes are Ready than the cell requires;
- achieved occupancy differs from target by more than the registered tolerance;
- loadgen has submission failures or client-side rate limiting;
- profiler has unobserved Pods;
- host saturation makes the load generator miss target QPS by more than 5%;
- test GPU demand exceeds remaining capacity unintentionally;
- the measurement window closes before an otherwise schedulable workload can drain.

Censoring caused intentionally by B2 is reported as an outcome, not silently dropped.
Latency quantiles use complete samples and always report the censored count beside them.

---

## 9. Decision Rules

### Confirm H1: PreFilter scan bottleneck

All of the following must hold:

1. B2 `prefilter_nodes_scanned` is at least 80% of `N` at P95.
2. B2 `t_prefilter` P95 is at least 2× B0.
3. `t_prefilter` is at least 10% of B2 `t_cycle` or `t_group_ready`.
4. Admitted Gang throughput falls, or `t_queue`/`t_after_submit` increases.
5. Bind latency and client submission validity remain stable.
6. CPU profile attributes material work to `gangFitsGPU`, snapshot listing, or their
   direct callees.

Then the permitted conclusion is:

> High occupancy forced TopoGang's whole-group capacity check into a near-full fleet
> scan. The resulting growth in `t_prefilter` became material to the scheduling cycle
> and limited admitted-Gang throughput.

### Reject H1

Reject the hypothesis when any of these holds:

- nodes scanned increases but `t_prefilter` remains below 10% of the critical path;
- `t_prefilter` stays flat while another term explains the latency growth;
- observed degradation is explained by client throttling, API Bind, host saturation, or
  an invalid capacity setup;
- N1/N2/N3 does not show growth with node count after B2 passes.

### Inconclusive

Report inconclusive when no Gang fits in B2, the profiler censors all Gangs, achieved
occupancy is uncontrolled, or the load generator cannot sustain the target QPS.

---

## 10. Expected Shapes — Not Results

The figures should be capable of showing:

```text
Figure A: achieved ρ → nodes scanned P95
Figure B: achieved ρ → t_prefilter P95
Figure C: QPS/time → admitted Gang/s and t_queue
Figure D: N → nodes scanned and t_prefilter (conditional validation)
Figure E: B0 versus B2 CPU profile top frames
```

Expected H1 shape:

```text
B0: nodes scanned ≪ N, t_prefilter small
B1: nodes scanned rises, t_prefilter begins to rise
B2: nodes scanned ≈ N, t_prefilter material, throughput/queue degrades
```

These are pre-registered expectations, not values to copy into the Results section.

---

## 11. Results

_Empty until B0/B1/B2 are executed._

### Run manifest

| Cell | Repeat | Achieved `ρ` | Valid? | Artifact directory |
| --- | ---: | ---: | --- | --- |
| B0 | 1 |  |  |  |
| B0 | 2 |  |  |  |
| B0 | 3 |  |  |  |
| B1 | 1 |  |  |  |
| B1 | 2 |  |  |  |
| B1 | 3 |  |  |  |
| B2 | 1 |  |  |  |
| B2 | 2 |  |  |  |
| B2 | 3 |  |  |  |

### Finding

_Not measured._

---

## 12. Follow-up Fix if H1 Is Confirmed

Evaluate one fix at a time against the same B0/B2 cells:

1. Maintain a scheduler-side aggregate of free GPU instead of scanning every node per
   Pod.
2. Cache the aggregate in `CycleState` so later extension points reuse one snapshot.
3. Bound or remove the advisory whole-cluster check and leave per-node correctness to
   Filter plus scheduler resource accounting.

The optimized version is accepted only when it reduces `t_prefilter` and improves
throughput without changing admission correctness, Gang completion, or topology
behavior.
