# Guide-Aligned Formal Experiment Runbook

These commands run the complete Catalog matrices, not the earlier readiness,
preview, smoke, or TopoGang-only load exercises.

The runners delete all Nodes in the current cluster. They refuse to proceed
unless the current context begins with `kwok-`, every existing Node is labeled
`type=kwok`, and `CONFIRM_DISPOSABLE_CLUSTER=yes` is set.

## Prerequisites

```bash
go test ./...
go vet ./...

kwokctl create cluster \
  --name gpu-scale \
  --runtime docker \
  --prometheus-port 9090

kubectl config use-context kwok-gpu-scale
curl -fsS http://127.0.0.1:9090/api/v1/status/buildinfo
```

Do not start `cmd/scheduler` separately. The Exp2 and Exp3 matrix runners start
and stop the required custom scheduler profiles themselves on port `10260`.

## 1. Exp1 — Control-Plane Scale Sweep

```bash
CONFIRM_DISPOSABLE_CLUSTER=yes make exp1-formal
```

Default matrix:

```text
N={100,500,1000,1500,2000} × 3 repeats = 15 runs
3,000 Pods/run at constant 50 Pod/s
```

Analyze:

```bash
python analysis/analyze_exp1.py \
  --input experiments/exp1-scale-sweep
```

## 2. Exp2 — Default vs TopoGang

Run Exp2 after Exp1 in the same disposable cluster. The runner resets the fleet
to 500 Nodes, creates one 63-GPU filler Pod per Node, alternates arm order, and
keeps the filler state fixed across each pair.

```bash
CONFIRM_DISPOSABLE_CLUSTER=yes make exp2-formal
```

Default matrix:

```text
seeds={42,43,44}
arms={default,topogang}
3 paired repeats = 6 runs
100 gangs × 8 members at Poisson 40 Pod/s
```

Generate the cross-experiment figures after all formal matrices exist:

```bash
python analysis/plot.py --experiments experiments --out analysis/figures
```

## 3. Recreate the cluster with simulated cold start

Exp3 requires a KWOK Node Stage, the delayed Pod-ready Stage, and a Pod-delete
Stage. Delete only the disposable lab cluster, render the complete configuration
with a 1–3 second Pod-ready delay, and recreate the cluster with that config:

```bash
kwokctl delete cluster --name gpu-scale

OUTPUT=/tmp/gpu-lab-exp3-stage.yaml \
  COLD_START_MIN_MS=1000 \
  COLD_START_MAX_MS=3000 \
  ./scripts/render-day6-cold-start.sh

kwokctl \
  --name gpu-scale \
  --config /tmp/gpu-lab-exp3-stage.yaml \
  create cluster \
  --runtime docker \
  --prometheus-port 9090

kubectl config use-context kwok-gpu-scale
```

The Exp3 runner submits a probe and refuses to continue unless the delayed Stage
is observable.

## 4. Exp3 — Burst and API-Server Pressure

```bash
CONFIRM_DISPOSABLE_CLUSTER=yes make exp3-formal
```

Default matrix:

```text
seeds={42,43,44}
arms={baseline,optimized}
3 paired repeats = 6 runs
zero preload; exactly 500 Creates released at t=30s
```

Analyze:

```bash
python analysis/analyze_exp3.py \
  --input experiments/exp3-burst
```

Finally regenerate the Catalog figures:

```bash
python analysis/plot.py --experiments experiments --out analysis/figures
```

## Reduced validation matrices

Use these only to validate a changed runner or environment. They are not formal
results:

```bash
CONFIRM_DISPOSABLE_CLUSTER=yes \
NODE_COUNTS="100 500" REPEATS=1 EXPECTED_PODS=100 SETTLE_SECONDS=1 \
make exp1-formal

CONFIRM_DISPOSABLE_CLUSTER=yes \
NODES=20 SEEDS="42" GANGS=4 CUTOFF_SECONDS=5 SETTLE_SECONDS=1 \
make exp2-formal

CONFIRM_DISPOSABLE_CLUSTER=yes \
NODES=20 SEEDS="42" EXPECTED_PODS=20 BURST_AT_SECONDS=2 \
OBSERVE_SECONDS=20 SETTLE_SECONDS=1 \
make exp3-formal
```

Always use a new `BATCH_ID` and immutable output directories. Reduced matrices
must never be mixed with the formal three-repeat dataset when writing conclusions.
