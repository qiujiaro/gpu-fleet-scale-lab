#!/usr/bin/env bash
# demo.sh — end-to-end small demo (~10-15 min): 200 nodes, steady + burst, produce figures.
# Day 10. Must run cleanly from scratch (interviewers will ask for reproducibility on the spot).
set -euo pipefail
cd "$(dirname "$0")/.."

echo "[1/8] create cluster (with prometheus)"
kwokctl create cluster --name demo --runtime docker --prometheus-port 9090
kubectl config use-context kwok-demo

echo "[2/8] spawn 200 GPU nodes"
scripts/spawn-nodes.sh 200

echo "[3/8] start topogang scheduler  (TODO: build ./cmd/scheduler first)"
# ./bin/scheduler --config config/scheduler/topogang-config.yaml --kubeconfig ~/.kube/config &

echo "[4/8] start profiler (watch first)"
# go run ./cmd/profiler --submit-log experiments/diagnostics/local/demo.jsonl --out experiments/diagnostics/local/demo.csv &

echo "[5/8] loadgen: steady + burst"
# go run ./cmd/loadgen --arrival burst --qps 20 --duration 90s --out experiments/diagnostics/local/demo.jsonl

echo "[6/8] compute P50/P95/P99 (profiler output)"
echo "[7/8] plot"
# python3 analysis/plot.py --in experiments/diagnostics/local/demo.csv --out analysis/figures/

echo "[8/8] Grafana at http://localhost:3000 ; cleanup with: kwokctl delete cluster --name demo"
echo "NOTE: uncommenting each step requires the Day 2-9 implementation to be done first."
