#!/usr/bin/env bash
# Exp2 phase A (implemented on Day 4): submit the same workload twice, half to default-scheduler
# and half to topogang, and confirm both halves get bound and neither scheduler touches
# the other's Pods.
#
# Prereq: the topogang scheduler is already running in another terminal —
#   go run ./cmd/scheduler --config config/scheduler/topogang-config.yaml \
#     --kubeconfig ~/.kube/config -v=4
set -euo pipefail

DUR="${DUR:-60}"
QPS="${QPS:-10}"
OUT_DIR="${OUT_DIR:-experiments/exp2-topogang/two-scheduler-isolation}"
mkdir -p "$OUT_DIR"

echo "== submitting ${QPS} qps for ${DUR}s to default-scheduler and topogang in parallel"
go run ./cmd/loadgen --arrival constant --qps "$QPS" --duration "$DUR" \
  --scheduler-name default-scheduler --seed 42 \
  --out "$OUT_DIR/default.jsonl" &
pid_default=$!
go run ./cmd/loadgen --arrival constant --qps "$QPS" --duration "$DUR" \
  --scheduler-name topogang --seed 43 \
  --out "$OUT_DIR/topogang.jsonl" &
pid_topo=$!
wait "$pid_default" "$pid_topo"

echo
echo "== settling"
sleep 15

echo
echo "== bound counts by scheduler (want: both non-zero, Pending zero)"
kubectl get pods -o json |
  jq -r '.items[]
         | [ .spec.schedulerName,
             (if (.spec.nodeName // "") == "" then "unbound" else "bound" end) ]
         | @tsv' |
  sort | uniq -c

echo
echo "== placement sample (want: topogang Pods on real nodes, same as default's)"
kubectl get pods -o wide --field-selector spec.schedulerName=topogang 2>/dev/null | head -10 ||
  kubectl get pods -o wide | head -10

echo
echo "== scheduler events for topogang Pods (want: Scheduled, source = your scheduler)"
kubectl get events --field-selector reason=Scheduled -o wide | tail -10
