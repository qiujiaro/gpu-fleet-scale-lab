# Build Log — GPU Fleet Scale Lab

> A 10-day, build-in-public engineering journal for the **[GPU Fleet Scale Lab](../../../)** — a Kubernetes control-plane scalability study that simulates **up to ~2,000 GPU nodes on a single laptop** (via [`kwok`](https://kwok.sigs.k8s.io/)) to quantify where AI-inference burst scale-out pressures the API server and scheduler.

Each entry is written the way I actually work: goals up front, real commands and errors in the middle, and a measured "what I learned" at the end. No polished-after-the-fact rewrites — the blockers and fixes are left in on purpose.

## What this demonstrates

- **Distributed-systems depth** — Kubernetes control plane internals: API server admission/watch paths, scheduler filtering/scoring/binding, etcd, and the [SIG-Scalability SLOs](https://github.com/kubernetes/community/blob/main/sig-scalability/slos/slos.md) (P99 latency, cluster-day availability).
- **Systems Go** — hand-written [load generator](../../../pkg/loadgen) (constant/Poisson/burst arrival, token-bucket, 429 backoff), [latency profiler](../../../pkg/profiler) (P50/P95/P99 with censored-sample handling), and a [custom scheduler plugin](../../../pkg/scheduler/plugins/topogang) on the Scheduling Framework (topology-aware Score + gang scheduling via PreFilter/Permit).
- **Experiment rigor** — every figure is generated from CSV, never hard-coded; fresh cluster per scale point, percentile reporting, and an explicit **honest-limitations** section (`kwok` simulates the control plane, *not* real GPUs/kubelets — simulated numbers are never dressed up as production).
- **Engineering discipline** — reproducible scripts, a documented methodology, and explicit local verification.

**Stack:** Go · Kubernetes · kwok · client-go · Scheduling Framework · Prometheus / PromQL · Docker

## Progress

| Day | Theme | Deliverable | Status |
| --- | --- | --- | --- |
| 1 | Environment + kwok cluster + SLO awareness | ~1–2k node cluster up/down; Prometheus wired | ✅ Done |
| 2 | Exp0 Load Generator + Calibration | Rate-controlled Pod creation and client-capacity preflight | ✅ Complete |
| 3 | Latency Profiler + first baseline sweep | Scheduling/binding breakdown, P50/P95/P99 | 🚧 In progress |
| 4 | Exp2 phase A: scheduler isolation | Second scheduler ownership and isolation check | ✅ Complete |
| 5 | Topology-aware gang plugin core algorithm | PreFilter + Score + Permit working | 🔲 Planned |
| 6 | Cold-start simulation + batched binding | Binding-throughput optimization | 🔲 Planned |
| 7 | Exp1 Fleet Readiness + Exp2 TopoGang | Readiness scale result + behavior/load results | ✅ Complete |
| 8 | Exp3 burst scale-out + API-server pressure | Burst load results | 🔲 Planned |
| 9 | Results compilation, charts, dashboard | Generated figures + dashboard | 🔲 Planned |
| 10 | README, demo, resume, interview, upstream | Full deliverables + write-up | 🔲 Planned |

**Latest:** Day 1 stood up a Docker-runtime kwok cluster, scaled the fake GPU pool to **2,000 nodes (all Ready in ≤ 3 s incrementally)**, and wired Prometheus scraping of API-server / scheduler metrics. → [Read Day 1](Day01.md)

## Read the logs

| Day | Log |
| --- | --- |
| 1 | [Day 1 — Environment + KWOK + SLOs](Day01.md) |
| 2 | [Day 2 — Exp0 Load Generator + Calibration](Day02.md) |
| 3 | [Day 3 — Latency Profiler](Day03.md) |
| 4 | [Day 4 — Exp2 scheduler isolation](Day04.md) |
| 5 | [Day 5 — Topology-aware gang plugin](Day05.md) |
| 6 | [Day 6 — Cold-start + batched binding](Day06.md) |
| 7 | [Day 7 — Exp1 Fleet Readiness + Exp2 TopoGang](Day07.md) |
| 8 | [Day 8 — Exp3 burst scale-out](Day08.md) |
| 9 | [Day 9 — Charts and dashboard](Day09.md) |
| 10 | [Day 10 — README, demo, wrap-up](Day10.md) |

---

<sub>Every entry follows the same frame (`_TEMPLATE.md`): **Goals → What I did → What I learned → Blockers & fixes → Open questions → Tomorrow**. Dates reflect when the work was actually done (~3–5 h/day).</sub>
