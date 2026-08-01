# Start Guide

## 1. Prerequisites

Required for the main workflow:

- Docker
- Go matching `go.mod`
- `kwokctl`
- `kubectl`
- `curl`, `jq`, `awk`, `sed`, and Bash
- Python 3 plus `analysis/requirements.txt` for figures

### Current toolchain and cluster environment
| Item | Observed value |
| --- | --- |
| Go | `go1.23.0` |
| kwokctl | `v0.8.0` |
| Docker Desktop | `4.65.0` (`221669`) |
| Docker Engine | `29.2.1`, API `1.53`, Linux `arm64` |
| Docker Desktop allocation | 8 CPUs, 4,108,828,672 bytes memory (~3.83 GiB) |
| containerd / runc | `v2.2.1` / `1.3.4` |
| Kubernetes client | `v1.34.1`, Kustomize `v5.7.1` |
| Kubernetes server | `v1.36.1`, Linux `arm64` |
| Python | `3.14.6` |
| jq | `jq-1.7.1-apple` |
| curl | `8.7.1` |
| Bash | GNU Bash `3.2.57` |
| Repository baseline | Git commit `565dfbe` |

The Kubernetes client is `v1.34.1` and the active server is `v1.36.1`. This exceeds
Kubernetes' supported client/server minor-version skew of ±1. Align the client with the
server (prefer a `v1.36.x` kubectl for this cluster) before publishing a new formal run;
otherwise record the mismatch as a threat to reproducibility.

Refresh the environment record before publishing a new performance run:

```bash
docker version
docker info --format 'cpus={{.NCPU}} memory_bytes={{.MemTotal}} os={{.OperatingSystem}} arch={{.Architecture}}'
kubectl version -o yaml
kwokctl --version
```

For each formal run, also record:

- Git commit SHA and whether the worktree was dirty;
- cluster name and active kubectl context;
- Kubernetes server version;
- Docker Desktop CPU and memory allocation;
- node count, advertised GPU capacity, QPS, repeats, gang size, and random seed;
- run start time and timezone.

Verify the local build before creating a cluster:

```bash
go build ./...
go test ./...
go test -race ./pkg/scheduler/plugins/topogang
python3 -m pip install -r analysis/requirements.txt
```

## 2. Create a disposable cluster

```bash
kwokctl create cluster \
  --name gpu-scale \
  --runtime docker \
  --prometheus-port 9090

kubectl config use-context kwok-gpu-scale
kubectl get --raw=/healthz
```

Always check the context before an experiment:

```bash
kubectl config current-context
kwokctl get clusters
```

Delete the cluster when finished:

```bash
kwokctl delete cluster --name gpu-scale
```

## 3. Prepare the initial fleet

Exp0 requires exactly 1,000 Ready nodes, and the committed Exp1 result uses the same
1,000-node fleet as its starting point:

```bash
./scripts/spawn-nodes.sh 1000
```

## 4. Exp0 — Load Generator and Client Calibration

Run Exp0 before Exp1 adds more nodes:

```bash
make exp0-loadgen-calibration
```

Defaults: 50 Pod/s for 60 seconds, at least 95% of target throughput, zero failed
submissions, and zero HTTP 429 responses.

Outputs are written to `experiments/exp0-loadgen/client-preflight/`. Override the defaults
when doing a local check:

```bash
EXPECTED_NODES=100 TARGET_QPS=10 DURATION_SEC=15 \
  RESULT_DIR=/tmp/client-calibration make exp0-loadgen-calibration
```

## 5. Exp1 — Fleet Readiness Scaling

After Exp0, Exp1 asks `kwokctl scale` to add scale-managed pools of 100, 500, and 1,000
nodes. Starting from the manually created fleet, this produces observed totals of
approximately 1,100, 1,500, and 2,000 nodes.

```bash
make exp1-fleet-readiness
```

Outputs:

```text
experiments/exp1-fleet-readiness/results.csv
experiments/exp1-fleet-readiness/nodes-100.txt
experiments/exp1-fleet-readiness/nodes-500.txt
experiments/exp1-fleet-readiness/nodes-1000.txt
```

Useful overrides:

```bash
CLUSTER=gpu-scale CONTEXT=kwok-gpu-scale TIMEOUT=180 \
  RESULTS_DIR=/tmp/exp1-results make exp1-fleet-readiness
```

The script overwrites `results.csv`. Use a fresh cluster for comparable results; existing
scale-managed nodes change the totals.

## 6. Start the TopoGang scheduler

Exp2 commands require the custom scheduler to run beside the default scheduler. Keep this
process open in a separate terminal:

```bash
go run ./cmd/scheduler \
  --config config/scheduler/topogang-config.yaml \
  --kubeconfig "$HOME/.kube/config" \
  -v=4
```

Verify its endpoints before continuing:

```bash
curl -ksSf https://127.0.0.1:10260/healthz
curl -ksSf https://127.0.0.1:10260/metrics >/dev/null
```

## 7. Exp2 phase A — Verify two-scheduler isolation

This diagnostic submits equal workloads to `default-scheduler` and `topogang` and prints
bound counts, placement, and scheduler events:

```bash
DUR=15 QPS=2 ./scripts/exp2-two-schedulers.sh
```

Outputs:

```text
experiments/exp2-topogang/two-scheduler-isolation/default.jsonl
experiments/exp2-topogang/two-scheduler-isolation/topogang.jsonl
```

The script does not delete the submitted Pods. Use a disposable cluster or clean them
before running capacity-sensitive experiments.

## 8. Exp2 behavior preview

Run the preview on a GPU-empty disposable cluster while TopoGang is running:

```bash
make exp2-topogang-preview
```

It creates one temporary 3-GPU node and two four-Pod groups. Expected result:

- default scheduler: 3/4 Pods placed;
- TopoGang: 0/4 Pods placed.

The script deletes its namespace and temporary node on exit. It refuses to run if another
GPU-capable node exists, because extra capacity would invalidate the comparison.

## 9. Exp2 profiling smoke

The smoke requires at least one Ready GPU node and the TopoGang health and metrics
endpoints:

```bash
./scripts/spawn-nodes.sh 20
make exp2-topogang-smoke
```

Defaults: 20 four-member gangs at 4 Pod/s. Results go to a timestamped directory under:

```text
experiments/exp2-topogang/controlled-load/local/
```

For a shorter check:

```bash
MAX_GANGS=4 TARGET_QPS=4 make exp2-topogang-smoke
```

By default, old Exp2 Pods are removed before the run and new Exp2 Pods are removed when
the script exits. Set `CLEANUP_OLD_PODS=false` or `CLEANUP_PODS=false` only when debugging.

## 10. Exp2 controlled-load matrix

The full default matrix runs 4, 8, 16, 32, and 64 Pod/s, three repeats per level, with
200 four-member gangs per run:

```bash
make exp2-topogang-load-test
```

This is substantially heavier than the smoke. Start with a reduced matrix when validating
a new environment:

```bash
QPS_LEVELS="4 8" REPEATS=1 MAX_GANGS=10 PAUSE_SECONDS=1 \
  make exp2-topogang-load-test
```

New runs are written under `controlled-load/local/`. The script records failed runs in
`summary.csv` and continues unless `FAIL_ON_RUN_FAILURE=true` is set.

Analyze a completed batch with:

```bash
python3 analysis/analyze_exp2.py \
  --input experiments/exp2-topogang/controlled-load/local/<batch-id>/summary.csv
```

## 11. Optional occupancy prefill

`exp2-topogang-prefill.sh` consumes GPU capacity with one-GPU filler Pods so a later Exp2
run can test a requested demand-to-remaining-capacity ratio:

```bash
./scripts/exp2-topogang-prefill.sh \
  --target-rho 0.8 \
  --expected-demand 80 \
  --run-id local-prefill
```

The script prints achieved occupancy as JSON. It does not remove filler Pods; delete its
namespace after the experiment:

```bash
kubectl delete namespace exp2-filler
```

## 12. Generate figures

```bash
make figures
```

The plotter reads available CSV files and skips figures whose required inputs are absent.
It does not invent placeholder data.

## Script status

| Script | Status |
| --- | --- |
| `spawn-nodes.sh` | Ready |
| `exp1-fleet-readiness.sh` | Ready; overwrites its result CSV |
| `exp0-loadgen-calibration.sh` | Ready; requires exactly the expected Ready-node count |
| `exp2-two-schedulers.sh` | Ready; leaves submitted Pods behind |
| `exp2-topogang-preview.sh` | Ready; destructive only to its temporary namespace/node |
| `exp2-topogang-smoke.sh` | Ready |
| `exp2-topogang-load-test.sh` | Ready; heavy with default settings |
| `exp2-topogang-prefill.sh` | Ready; filler cleanup is manual |
| `demo.sh` | Scaffold only; several execution lines remain commented out |

`make smoke` is also still a placeholder and should not be treated as an end-to-end test.
