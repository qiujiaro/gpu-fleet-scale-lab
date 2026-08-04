#!/usr/bin/env bash
# Guide Exp3: paired exact 500-Pod bursts, DefaultBinder vs CoalescedBind.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"
# shellcheck source=scripts/lib/formal.sh
source "${REPO_ROOT}/scripts/lib/formal.sh"
# shellcheck source=scripts/lib/telemetry.sh
source "${REPO_ROOT}/scripts/lib/telemetry.sh"

NODES="${NODES:-1000}"
SEEDS="${SEEDS:-42 43 44}"
EXPECTED_PODS="${EXPECTED_PODS:-500}"
BURST_AT_SECONDS="${BURST_AT_SECONDS:-30}"
OBSERVE_SECONDS="${OBSERVE_SECONDS:-120}"
SETTLE_SECONDS="${SETTLE_SECONDS:-60}"
NAMESPACE="${NAMESPACE:-default}"
BATCH_ID="${BATCH_ID:-exp3-burst-$(date +%Y%m%d-%H%M%S)-$$}"
RESULT_ROOT="${RESULT_ROOT:-experiments/exp3-burst}"
SCHEDULER_PORT="${SCHEDULER_PORT:-10260}"
SCHEDULER_URL="${SCHEDULER_URL:-https://127.0.0.1:${SCHEDULER_PORT}}"
SCHEDULER_BIN="${SCHEDULER_BIN:-${REPO_ROOT}/bin/scheduler}"
KUBECONFIG_PATH="${KUBECONFIG_PATH:-${KUBECONFIG:-${HOME}/.kube/config}}"
TELEMETRY_PROM_URL="${TELEMETRY_PROM_URL:-http://127.0.0.1:9090}"
TELEMETRY_PROM_STEP="${TELEMETRY_PROM_STEP:-1s}"
TELEMETRY_HOST_INTERVAL="${TELEMETRY_HOST_INTERVAL:-1s}"

formal_profiler_pid=""
formal_scheduler_pid=""
current_run_id=""
cleanup() {
  local rc=$?
  if [[ -n "${formal_profiler_pid}" ]] && kill -0 "${formal_profiler_pid}" 2>/dev/null; then
    kill -TERM "${formal_profiler_pid}" 2>/dev/null || true
    wait "${formal_profiler_pid}" 2>/dev/null || true
  fi
  telemetry_stop >/dev/null 2>&1 || true
  formal_stop_scheduler
  if [[ -n "${current_run_id}" ]]; then
    formal_delete_run_pods "${NAMESPACE}" "${current_run_id}" >/dev/null 2>&1 || true
  fi
  exit "${rc}"
}
trap cleanup EXIT INT TERM

formal_require_commands kubectl go curl awk sed
formal_require_disposable_cluster
curl --fail --silent --show-error "${TELEMETRY_PROM_URL}/api/v1/status/buildinfo" >/dev/null ||
  formal_fail "Prometheus is unavailable at ${TELEMETRY_PROM_URL}"

mkdir -p bin "${RESULT_ROOT}"
GOCACHE="${GOCACHE:-/tmp/gpu-fleet-formal-go-cache}" go build -o bin/loadgen ./cmd/loadgen
GOCACHE="${GOCACHE:-/tmp/gpu-fleet-formal-go-cache}" go build -o bin/profiler ./cmd/profiler
GOCACHE="${GOCACHE:-/tmp/gpu-fleet-formal-go-cache}" go build -o "${SCHEDULER_BIN}" ./cmd/scheduler
kubectl delete pods -n "${NAMESPACE}" --all --ignore-not-found --wait=true >/dev/null
kubectl delete namespace exp2-filler --ignore-not-found --wait=true >/dev/null

echo "== Exp3: reset fleet to ${NODES} Nodes =="
formal_reset_nodes "${NODES}"
sleep "${SETTLE_SECONDS}"

echo "== Exp3: verify simulated cold-start Stage =="
probe="exp3-coldstart-probe-$$"
probe_start="$(date +%s)"
kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${probe}
  namespace: ${NAMESPACE}
  labels:
    gpu-lab/simulated-cold-start: "true"
spec:
  schedulerName: default-scheduler
  restartPolicy: Never
  containers:
  - name: probe
    image: registry.k8s.io/pause:3.9
EOF
kubectl wait -n "${NAMESPACE}" --for=condition=Ready "pod/${probe}" --timeout=10s >/dev/null ||
  formal_fail "cold-start probe did not become Ready"
probe_elapsed=$(( $(date +%s) - probe_start ))
kubectl delete pod -n "${NAMESPACE}" "${probe}" --wait=true >/dev/null
[[ "${probe_elapsed}" -ge 1 && "${probe_elapsed}" -le 5 ]] ||
  formal_fail "simulated cold-start Stage is not active: probe elapsed ${probe_elapsed}s, want 1-5s"

matrix="${RESULT_ROOT}/${BATCH_ID}-matrix.csv"
echo "batch_id,seed,order,arm,run_id,status,result_dir" >"${matrix}"

run_arm() {
  local arm="$1"
  local seed="$2"
  local order="$3"
  local scheduler_name config result_dir
  if [[ "${arm}" == "baseline" ]]; then
    scheduler_name="day6-baseline"
    config="${REPO_ROOT}/config/scheduler/day6-baseline-config.yaml"
  else
    scheduler_name="day6-optimized"
    config="${REPO_ROOT}/config/scheduler/day6-optimized-config.yaml"
  fi
  current_run_id="${BATCH_ID}-s${seed}-${arm}"
  result_dir="${RESULT_ROOT}/${arm}/${current_run_id}"
  [[ ! -e "${result_dir}" ]] || formal_fail "result directory exists: ${result_dir}"
  mkdir -p "${result_dir}"

  formal_start_scheduler "${config}" "${result_dir}/scheduler.log"
  curl -ksSf "${SCHEDULER_URL}/metrics" >"${result_dir}/scheduler-before.metrics"
  telemetry_start \
    "${current_run_id}" exp3 "${arm}" "${result_dir}/pods" \
    "nodes=${NODES}" "scheduler=${scheduler_name}" \
    "optimize=$([[ "${arm}" == "optimized" ]] && echo true || echo false)" \
    "arrival=burst" "qps=1000" "seed=${seed}" "expected_pods=${EXPECTED_PODS}" \
    "client_qps=1000" "client_burst=1000" "burst_at_seconds=${BURST_AT_SECONDS}" \
    "workload_duration_seconds=${OBSERVE_SECONDS}" \
    "simulated_cold_start=true" "cold_start_min_ms=1000" "cold_start_max_ms=3000" \
    "namespace=${NAMESPACE}"

  bin/profiler \
    --namespace "${NAMESPACE}" \
    --label-selector "exp2.dev/run-id=${current_run_id}" \
    --submit-log "${result_dir}/submit.jsonl" \
    --out "${result_dir}/pods.csv" \
    >"${result_dir}/profiler.log" 2>&1 &
  formal_profiler_pid=$!
  formal_wait_profiler "${formal_profiler_pid}" "${result_dir}/profiler.log"

  run_started="$(date +%s)"
  bin/loadgen \
    --namespace "${NAMESPACE}" --scheduler-name "${scheduler_name}" \
    --run-id "${current_run_id}" --simulated-cold-start \
    --arrival burst --qps 1000 --burst 1000 \
    --client-qps 1000 --client-burst 1000 \
    --spike-count "${EXPECTED_PODS}" --burst-at "${BURST_AT_SECONDS}s" \
    --preload-qps 0 \
    --duration "${OBSERVE_SECONDS}" --max-pods "${EXPECTED_PODS}" \
    --seed "${seed}" --out "${result_dir}/submit.jsonl" \
    >"${result_dir}/loadgen.log" 2>&1

  deadline=$(( run_started + OBSERVE_SECONDS ))
  while true; do
    ready="$(kubectl get pods -n "${NAMESPACE}" -l "exp2.dev/run-id=${current_run_id}" \
      --no-headers 2>/dev/null |
      awk '$2 == "1/1" && $3 == "Running" {count++} END {print count+0}')"
    [[ "${ready}" -ge "${EXPECTED_PODS}" ]] && break
    [[ $(date +%s) -lt "${deadline}" ]] || break
    sleep 1
  done
  sleep 10
  kill -TERM "${formal_profiler_pid}"
  wait "${formal_profiler_pid}"
  formal_profiler_pid=""
  curl -ksSf "${SCHEDULER_URL}/metrics" >"${result_dir}/scheduler-after.metrics"
  telemetry_stop
  telemetry_validate "${result_dir}/pods"

  submitted="$(wc -l <"${result_dir}/submit.jsonl" | tr -d ' ')"
  observed=$(( $(wc -l <"${result_dir}/pods.csv") - 1 ))
  censored="$(awk -F, 'NR>1 && $15=="true" {n++} END {print n+0}' "${result_dir}/pods.csv")"
  submit_span="$(sed -n 's/.*first-to-last-success=\([0-9.][0-9.]*\)s.*/\1/p' "${result_dir}/loadgen.log" | tail -n 1)"
  [[ "${submitted}" -eq "${EXPECTED_PODS}" ]] || formal_fail "submitted=${submitted}, want ${EXPECTED_PODS}"
  [[ "${observed}" -eq "${EXPECTED_PODS}" ]] || formal_fail "observed=${observed}, want ${EXPECTED_PODS}"
  [[ "${censored}" -eq 0 ]] || formal_fail "censored=${censored}, want 0"
  grep -q "succeeded=${EXPECTED_PODS} failed=0 rate-limited=0" "${result_dir}/loadgen.log" ||
    formal_fail "loadgen validity check failed; see ${result_dir}/loadgen.log"
  [[ -n "${submit_span}" ]] || formal_fail "loadgen success span is missing"
  awk -v span="${submit_span}" 'BEGIN {exit !(span <= 5)}' ||
    formal_fail "burst Create span ${submit_span}s exceeds 5s"
  echo "${BATCH_ID},${seed},${order},${arm},${current_run_id},PASS,${result_dir}" >>"${matrix}"

  formal_delete_run_pods "${NAMESPACE}" "${current_run_id}"
  current_run_id=""
  formal_stop_scheduler
}

repeat=0
for seed in ${SEEDS}; do
  repeat=$(( repeat + 1 ))
  if (( repeat % 2 == 1 )); then
    order="baseline-optimized"
    run_arm baseline "${seed}" "${order}"
    run_arm optimized "${seed}" "${order}"
  else
    order="optimized-baseline"
    run_arm optimized "${seed}" "${order}"
    run_arm baseline "${seed}" "${order}"
  fi
done

echo "PASS: Exp3 paired matrix complete: ${matrix}"
echo "Analyze: python analysis/analyze_exp3.py --input ${RESULT_ROOT}"
