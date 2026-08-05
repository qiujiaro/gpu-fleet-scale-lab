#!/usr/bin/env bash
# Shared safety and lifecycle helpers for the guide-aligned formal runners.

formal_fail() {
  echo "FAIL: $*" >&2
  return 1
}

formal_require_commands() {
  local command
  for command in "$@"; do
    command -v "${command}" >/dev/null || formal_fail "${command} is required"
  done
}

formal_wait_api_server() {
  local timeout="${API_SERVER_READY_TIMEOUT_SECONDS:-120}"
  local deadline=$(( $(date +%s) + timeout ))

  while ! kubectl get --raw=/readyz >/dev/null 2>&1; do
    if [[ $(date +%s) -ge "${deadline}" ]]; then
      kubectl get --raw='/readyz?verbose' >&2 || true
      formal_fail "Kubernetes API server did not become ready within ${timeout}s"
      return 1
    fi
    sleep 1
  done
}

formal_require_disposable_cluster() {
  [[ "${CONFIRM_DISPOSABLE_CLUSTER:-}" == "yes" ]] ||
    formal_fail "set CONFIRM_DISPOSABLE_CLUSTER=yes; formal runners delete all Nodes in the current cluster"
  local context
  context="$(kubectl config current-context)"
  [[ "${context}" == kwok-* ]] ||
    formal_fail "current context ${context} is not a kwok-* context"
  formal_wait_api_server
  local non_kwok
  non_kwok="$(
    kubectl get nodes -l 'type!=kwok' --no-headers 2>/dev/null |
      awk 'NF {count++} END {print count+0}'
  )"
  [[ "${non_kwok}" -eq 0 ]] ||
    formal_fail "current cluster contains ${non_kwok} Node(s) not labeled type=kwok"
}

formal_reset_nodes() {
  local count="$1"
  kubectl delete nodes --all --ignore-not-found --wait=false >/dev/null
  local delete_deadline=$(( $(date +%s) + ${NODE_DELETE_TIMEOUT_SECONDS:-180} ))
  while true; do
    local remaining
    remaining="$(kubectl get nodes --no-headers 2>/dev/null | awk 'NF {count++} END {print count+0}')"
    [[ "${remaining}" -eq 0 ]] && break
    if [[ $(date +%s) -ge "${delete_deadline}" ]]; then
      formal_fail "timed out deleting Nodes: ${remaining} remain"
      return 1
    fi
    sleep 1
  done

  "${REPO_ROOT}/scripts/spawn-nodes.sh" "${count}" >/dev/null
  local deadline=$(( $(date +%s) + ${NODE_READY_TIMEOUT_SECONDS:-180} ))
  while true; do
    local total ready
    total="$(kubectl get nodes --no-headers | awk 'NF {count++} END {print count+0}')"
    ready="$(kubectl get nodes --no-headers | awk '$2 == "Ready" {count++} END {print count+0}')"
    if [[ "${total}" -eq "${count}" && "${ready}" -eq "${count}" ]]; then
      break
    fi
    if [[ $(date +%s) -ge "${deadline}" ]]; then
      formal_fail "wanted ${count}/${count} Ready Nodes, got ${ready}/${total}"
      return 1
    fi
    sleep 1
  done
}

formal_wait_profiler() {
  local pid="$1"
  local log_file="$2"
  local _
  for _ in $(seq 1 100); do
    grep -q 'profiler: watching' "${log_file}" && return 0
    kill -0 "${pid}" 2>/dev/null ||
      formal_fail "profiler exited before watch startup; see ${log_file}"
    sleep 0.1
  done
  formal_fail "profiler watch startup timed out; see ${log_file}"
}

formal_start_scheduler() {
  local config="$1"
  local log_file="$2"
  local runtime_config="${log_file%.log}.config.yaml"
  local escaped_kubeconfig
  [[ -z "${formal_scheduler_pid:-}" ]] ||
    formal_fail "a formal-run scheduler is already active"
  if curl -ksSf "${SCHEDULER_URL}/healthz" >/dev/null 2>&1; then
    formal_fail "${SCHEDULER_URL} is already occupied; stop the existing custom scheduler"
  fi

  # When --config is set, kube-scheduler reads clientConnection.kubeconfig from
  # that file instead of applying the --kubeconfig flag to the component config.
  # Render the caller-selected kubeconfig into an immutable per-run artifact.
  escaped_kubeconfig="${KUBECONFIG_PATH//\\/\\\\}"
  escaped_kubeconfig="${escaped_kubeconfig//\"/\\\"}"
  awk -v kubeconfig="${escaped_kubeconfig}" '
    /^clientConnection:[[:space:]]*$/ {
      print
      print "  kubeconfig: \"" kubeconfig "\""
      in_client_connection = 1
      next
    }
    in_client_connection && /^  kubeconfig:[[:space:]]*/ {
      next
    }
    in_client_connection && /^[^[:space:]#]/ {
      in_client_connection = 0
    }
    { print }
  ' "${config}" >"${runtime_config}"

  "${SCHEDULER_BIN}" \
    --config "${runtime_config}" \
    --kubeconfig "${KUBECONFIG_PATH}" \
    --secure-port "${SCHEDULER_PORT}" \
    --authorization-always-allow-paths=/healthz,/readyz,/livez,/metrics \
    -v=3 >"${log_file}" 2>&1 &
  formal_scheduler_pid=$!
  local _
  for _ in $(seq 1 200); do
    if curl -ksSf "${SCHEDULER_URL}/healthz" >/dev/null 2>&1 &&
       curl -ksSf "${SCHEDULER_URL}/metrics" >/dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "${formal_scheduler_pid}" 2>/dev/null; then
      wait "${formal_scheduler_pid}" || true
      formal_scheduler_pid=""
      sed -n '1,160p' "${log_file}" >&2
      formal_fail "scheduler exited during startup"
      return 1
    fi
    sleep 0.25
  done
  formal_stop_scheduler
  formal_fail "scheduler did not become healthy at ${SCHEDULER_URL}"
}

formal_stop_scheduler() {
  [[ -n "${formal_scheduler_pid:-}" ]] || return 0
  local pid="${formal_scheduler_pid}"
  formal_scheduler_pid=""
  kill -TERM "${pid}" 2>/dev/null || true
  wait "${pid}" 2>/dev/null || true
}

formal_delete_run_pods() {
  local namespace="$1"
  local run_id="$2"
  kubectl delete pods -n "${namespace}" \
    -l "exp2.dev/run-id=${run_id}" \
    --ignore-not-found --wait=true >/dev/null
}

formal_validate_loadgen() {
  local log_file="$1"
  local expected="$2"
  local target_qps="$3"
  grep -q "succeeded=${expected} failed=0 rate-limited=0" "${log_file}" ||
    formal_fail "loadgen validity check failed; see ${log_file}"
  local span actual_qps
  span="$(sed -n 's/.*first-to-last-success=\([0-9.][0-9.]*\)s.*/\1/p' "${log_file}" | tail -n 1)"
  [[ -n "${span}" ]] || formal_fail "loadgen success span is missing in ${log_file}"
  actual_qps="$(awk -v pods="${expected}" -v seconds="${span}" \
    'BEGIN {if (seconds <= 0) exit 1; printf "%.6f", (pods-1)/seconds}')"
  awk -v actual="${actual_qps}" -v target="${target_qps}" \
    'BEGIN {ratio=actual/target; exit !(ratio >= 0.95 && ratio <= 1.05)}' ||
    formal_fail "achieved QPS ${actual_qps} differs from target ${target_qps} by more than 5%"
}
