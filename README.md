# GPU Fleet Scale Lab

**A Kubernetes control-plane scalability lab for simulated AI/GPU fleets.** It uses a
real Kubernetes API server, scheduler, etcd, client-go, and Scheduling Framework plugin
with KWOK-simulated nodes and GPUs.

Go · [KWOK](https://kwok.sigs.k8s.io/) · client-go · Kubernetes Scheduling Framework · Prometheus

## Scope

| Real | Simulated | Not covered |
| --- | --- | --- |
| kube-apiserver, etcd, schedulers, controllers, client-go request/watch paths | Nodes, kubelets, GPU capacity, cold-start delay | Real GPUs, runtimes, image pulls, CNI, storage, NVLink/IB fabric |

Results describe control-plane behavior in this local simulation. They are not claims
about production GPU or data-plane performance.

## Experiments

| ID | Experiment | Status |
| --- | --- | --- |
| Exp0 | Load Generator and Client Calibration | Complete |
| Exp1 | Fleet Readiness Scaling to 2,000 simulated nodes | Complete |
| Exp2 | TopoGang scheduler isolation, gang behavior, and 4–64 Pod/s load profiling | Complete within the tested range |
| Exp3 | Burst and API Server Pressure | Planned follow-up |

Detailed measurements, methodology, limitations, and conclusions are in the
**[Performance & Scalability Report](docs/REPORT.md)**. Canonical experiment names and
data locations are in the [experiment catalog](docs/experiments/CATALOG.md).

## Quickstart

See the **[Start Guide](docs/START.md)** for prerequisites, environment information,
cluster lifecycle, scheduler startup, experiment parameters, outputs, and cleanup.

```bash
go build ./...
go test ./...

kwokctl create cluster --name gpu-scale --runtime docker --prometheus-port 9090
kubectl config use-context kwok-gpu-scale
./scripts/spawn-nodes.sh 1000

make exp0-loadgen-calibration
make exp1-fleet-readiness
```

Exp2 additionally requires the TopoGang scheduler to run in a separate terminal; follow
the Start Guide before running:

```bash
make exp2-two-schedulers
make exp2-topogang-preview
make exp2-topogang-smoke
make exp2-topogang-load-test
```

## Components

| Component | Purpose |
| --- | --- |
| [`cmd/loadgen`](cmd/loadgen) / [`pkg/loadgen`](pkg/loadgen) | Rate-controlled Pod submission, retry, and lifecycle recording |
| [`cmd/profiler`](cmd/profiler) / [`pkg/profiler`](pkg/profiler) | Watch-based Pod and gang timelines, censoring, and quantiles |
| [`cmd/scheduler`](cmd/scheduler) / [`pkg/scheduler/plugins/topogang`](pkg/scheduler/plugins/topogang) | Out-of-tree scheduler with TopoGang PreFilter, Filter, Score, Reserve, and Permit |
| [`analysis`](analysis) | CSV analysis and generated figures |
| [`scripts`](scripts) | Exp0/Exp1/Exp2 orchestration |

## Repository layout

```text
cmd/                         executable entrypoints
pkg/                         load generator, profiler, and TopoGang implementation
config/                      KWOK node and scheduler configuration
scripts/                     experiment runners
experiments/
  exp0-loadgen/              Exp0 calibration
  exp1-fleet-readiness/      Exp1 results
  exp2-topogang/             Exp2 isolation, behavior, and load data
analysis/                    analysis code and generated figures
docs/                        start guide, report, designs, and engineering logs
```

## Documentation

- [Architecture](docs/ARCHITECTURE.md) — system boundaries, runtime components, and data flow
- [Start Guide](docs/START.md) — how to build and run the lab
- [Performance & Scalability Report](docs/REPORT.md) — results, methodology, and validity limits
- [Experiment Catalog](docs/experiments/CATALOG.md) — canonical names, scope, status, and data layout
- [Formal Experiment Runbook](docs/FORMAL_RUNBOOK.md) — one-command Exp1/Exp2/Exp3 matrices and run order
- [Exp2 Load Profiling Protocol](docs/experiments/exp2-topogang-load-profiling.md)
- [Engineering Build Log](docs/notes/logs/BUILD_LOG.md)

## Development

```bash
make build
make test
make vet
go test -race ./pkg/scheduler/plugins/topogang
```

Performance experiments are run explicitly on a controlled local environment; the
development commands above provide the lightweight local verification path.
