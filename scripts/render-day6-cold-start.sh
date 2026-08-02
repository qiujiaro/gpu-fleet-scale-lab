#!/usr/bin/env bash
# Render a KWOK Stage config for cluster creation. The output is configuration for the
# KWOK controller, not a Kubernetes object to apply with kubectl in a kwokctl cluster.
set -euo pipefail

cd "$(dirname "$0")/.."

MIN_MS="${COLD_START_MIN_MS:-1000}"
MAX_MS="${COLD_START_MAX_MS:-3000}"
OUTPUT="${OUTPUT:-/tmp/gpu-lab-day6-cold-start.yaml}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ "${MIN_MS}" =~ ^[0-9]+$ ]] || fail "COLD_START_MIN_MS must be a non-negative integer"
[[ "${MAX_MS}" =~ ^[0-9]+$ ]] || fail "COLD_START_MAX_MS must be a non-negative integer"
(( MAX_MS >= MIN_MS )) || fail "COLD_START_MAX_MS must be >= COLD_START_MIN_MS"

sed \
  -e "s/COLD_START_MIN_MS/${MIN_MS}/g" \
  -e "s/COLD_START_MAX_MS/${MAX_MS}/g" \
  config/kwok/pod-cold-start-stage.yaml >"${OUTPUT}"

echo "Rendered simulated cold-start Stage: ${OUTPUT}"
echo "Use it when creating/restarting the KWOK controller; do not kubectl apply it in a kwokctl cluster."
