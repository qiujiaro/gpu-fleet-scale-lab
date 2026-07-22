# GPU Fleet Scale Lab

A Kubernetes control-plane scalability & GPU cold-start lab that simulates **up to ~2,000 GPU nodes** on a single laptop via [`kwok`](https://kwok.sigs.k8s.io/), to quantify where AI-inference burst scale-out pressures the API server and scheduler.

## ⚠️ Honest Limitations (read first)

- `kwok` simulates the **control plane** (apiserver / scheduler / controller / watch timing) — **not** real kubelets, container runtimes, image pulls, GPUs, or networking.
- "Cold-start latency" here is a **configurable simulated delay**, not a real image pull.
- Single-machine scale tops out around **~1k node / ~100k pod** territory (kwok's own docs); larger scale is discussed as **trend extrapolation only**.
- Simulated numbers are **never** presented as real-hardware or production performance.

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Control plane in focus; data plane is abstracted by kwok into "pod becomes Ready after a configurable delay."

## Core modules (hand-written)

1. **Load Generator** ([pkg/loadgen](pkg/loadgen)) — constant/poisson/burst arrival + token bucket + 429 handling.
2. **Latency Profiler** ([pkg/profiler](pkg/profiler)) — scheduling/binding/cold-start breakdown, P50/P95/P99, censored-sample handling.
3. **TopoGang Scheduler Plugin** ([pkg/scheduler/plugins/topogang](pkg/scheduler/plugins/topogang)) — topology-aware Score + gang (PreFilter/Permit) on the Scheduling Framework.

## Quickstart

```bash
go build ./... && go test ./...      # already passes (skeleton + implemented arrival / quantile)
./scripts/demo.sh                    # end-to-end demo (fill in the implementation per the 10-day plan first)
```

## Experiments

- **Exp1** Scale sweep (node count → scheduling P99 / throughput)
- **Exp2** Gang vs default scheduler under contention
- **Exp3** Burst scale-out & API-server pressure (optimize on/off)

Methodology + results: [docs/REPORT.md](docs/REPORT.md). All figures are generated from CSV in [experiments/](experiments); no hard-coded numbers.

## Repository layout

See the 10-day guide (`../10-Day_Hands-On_Project_Guide.md`) §6.2.

## References

kwok, SIG Scalability SLOs, Scheduling Framework, scheduler-plugins, API Priority & Fairness — links in the 10-day guide §10.
