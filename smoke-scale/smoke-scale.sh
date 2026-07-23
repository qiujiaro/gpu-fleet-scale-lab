#!/usr/bin/env bash
set -euo pipefail

# Cluster to scale. The kubectl context kwokctl creates is "kwok-<name>",
# so we MUST read nodes through that same context or we scale one cluster
# and measure another.
CLUSTER="${CLUSTER:-gpu-scale}"
CONTEXT="${CONTEXT:-kwok-${CLUSTER}}"
TIMEOUT="${TIMEOUT:-120}"   # max seconds to wait for a target before giving up

# Write all outputs into ./results next to this script, regardless of CWD.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${RESULTS_DIR:-${SCRIPT_DIR}/results}"
RESULTS="${RESULTS:-${RESULTS_DIR}/smoke-scale-results.csv}"
mkdir -p "$RESULTS_DIR"

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

echo
column -s, -t "$RESULTS"
