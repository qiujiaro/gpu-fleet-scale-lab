#!/usr/bin/env bash
# spawn-nodes.sh N — inject N fake GPU nodes (with GPU count and topology labels).
# Day 1. Understanding the labels/capacity and topology grouping is an interview point
# (how you model GPU topology).
set -euo pipefail

N="${1:?usage: spawn-nodes.sh <node-count>}"
TEMPLATE="$(dirname "$0")/../config/kwok/node-template.yaml"
DOMAINS=8   # number of nvlink-domains, multiple nodes per domain
ZONES=3

for i in $(seq 1 "$N"); do
  dom=$(( i % DOMAINS ))
  zone=$(( i % ZONES ))
  sed -e "s/PLACEHOLDER_DOMAIN/${dom}/g" \
      -e "s/PLACEHOLDER_ZONE/${zone}/g" \
      -e "s/gpu-node-PLACEHOLDER/gpu-node-${i}/g" \
      "$TEMPLATE"
  echo "---"
done | kubectl apply -f - >/dev/null

echo "spawned ${N} nodes"
kubectl get nodes --no-headers | wc -l
