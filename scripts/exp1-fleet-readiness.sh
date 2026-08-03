#!/usr/bin/env bash
set -euo pipefail

# Cluster to scale. The kubectl context kwokctl creates is "kwok-<name>",
# so we MUST read nodes through that same context or we scale one cluster
# and measure another.
CLUSTER="${CLUSTER:-gpu-scale}"
CONTEXT="${CONTEXT:-kwok-${CLUSTER}}"
TIMEOUT="${TIMEOUT:-120}"   # max seconds to wait for a target before giving up

# Scripts orchestrate; data lives under experiments/. Resolve the repo root from this
# script's own location so the output path holds regardless of CWD.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
RESULTS_DIR="${RESULTS_DIR:-${REPO_ROOT}/experiments/exp1-fleet-readiness}"
RESULTS="${RESULTS:-${RESULTS_DIR}/results.csv}"
mkdir -p "$RESULTS_DIR"

# This legacy readiness experiment creates Nodes rather than Pods, so the common
# Pod-Create PromQL is disabled unless the caller explicitly supplies a Prometheus URL.
if [[ -z "${TELEMETRY_PROM_URL+x}" ]]; then
  TELEMETRY_PROM_URL=""
fi
# shellcheck source=scripts/lib/telemetry.sh
source "${SCRIPT_DIR}/lib/telemetry.sh"

kube() { kubectl --context "$CONTEXT" "$@"; }

# --- Preflight: fail loudly instead of looping forever -----------------------
if ! kwokctl get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "ERROR: kwok cluster '$CLUSTER' does not exist. Run: kwokctl get clusters" >&2
  exit 1
fi
if ! kubectl config get-contexts -o name 2>/dev/null | grep -qx "$CONTEXT"; then
  echo "ERROR: kube context '$CONTEXT' not found. Run: kubectl config get-contexts" >&2
  exit 1
fi

existing=$(kube get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')
if [ "$existing" -ne 0 ]; then
  echo "WARNING: cluster '$CLUSTER' already has ${existing} node(s)." >&2
  echo "         'kwokctl scale' adds scale-managed nodes on top of these," >&2
  echo "         so counts will not equal the target. Use a fresh cluster for clean numbers." >&2
fi

RUN_ID="${RUN_ID:-exp1-readiness-$(date +%Y%m%d-%H%M%S)-$$}"
TELEMETRY_PREFIX="${TELEMETRY_PREFIX:-${RESULTS%.csv}}"

cleanup() {
  local rc=$?
  if ! telemetry_stop; then
    [[ "${rc}" -ne 0 ]] || rc=1
  fi
  exit "${rc}"
}
trap cleanup EXIT INT TERM

telemetry_start \
  "${RUN_ID}" exp1 fleet-readiness "${TELEMETRY_PREFIX}" \
  "cluster=${CLUSTER}" \
  "context=${CONTEXT}" \
  "start_nodes=${existing}" \
  "targets=100,500,1000" \
  "scheduler=default-scheduler" \
  "optimize=false" \
  "timeout_seconds=${TIMEOUT}"

echo "target,start_nodes,total_nodes,ready_nodes,ready_seconds" > "$RESULTS"

for target in 100 500 1000; do
  start_nodes=$(kube get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')
  start_time=$(date +%s)

  echo "Scaling '${CLUSTER}' from ${start_nodes} to ${target} nodes..."
  kwokctl scale node --name="$CLUSTER" --replicas="$target"

  total=0; ready=0
  while true; do
    node_output=$(kube get nodes --no-headers 2>/dev/null || true)

    total=$(printf '%s\n' "$node_output" | awk 'NF {c++} END {print c+0}')
    ready=$(printf '%s\n' "$node_output" | awk '$2 ~ /^Ready/ {c++} END {print c+0}')

    printf "\rtarget=%s total=%s ready=%s" "$target" "$total" "$ready"

    # Done when at least 'target' nodes are Ready (>=, so pre-existing or
    # extra nodes never cause an infinite loop).
    if [ "$ready" -ge "$target" ]; then
      break
    fi

    if [ $(( $(date +%s) - start_time )) -ge "$TIMEOUT" ]; then
      printf "\n"
      echo "TIMEOUT after ${TIMEOUT}s at target=${target} (ready=${ready})" >&2
      break
    fi

    sleep 1
  done

  end_time=$(date +%s)
  elapsed=$((end_time - start_time))

  echo
  echo "${target},${start_nodes},${total},${ready},${elapsed}" >> "$RESULTS"
  echo "Completed in ${elapsed}s"

  kube get nodes > "${RESULTS_DIR}/nodes-${target}.txt"
done

telemetry_stop
telemetry_validate "${TELEMETRY_PREFIX}"

echo
column -s, -t "$RESULTS"
