#!/usr/bin/env bash
# Guide Exp1: N={100,500,1000,1500,2000}, three exact 3,000-Pod runs per cell.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"
# shellcheck source=scripts/lib/formal.sh
source "${REPO_ROOT}/scripts/lib/formal.sh"
# shellcheck source=scripts/lib/telemetry.sh
source "${REPO_ROOT}/scripts/lib/telemetry.sh"

NODE_COUNTS="${NODE_COUNTS:-100 500 1000 1500 2000}"
REPEATS="${REPEATS:-3}"
EXPECTED_PODS="${EXPECTED_PODS:-3000}"
TARGET_QPS="${TARGET_QPS:-50}"
SEED="${SEED:-42}"
SETTLE_SECONDS="${SETTLE_SECONDS:-60}"
NAMESPACE="${NAMESPACE:-default}"
BATCH_ID="${BATCH_ID:-exp1-scale-$(date +%Y%m%d-%H%M%S)-$$}"
RESULT_ROOT="${RESULT_ROOT:-experiments/exp1-scale-sweep}"
TELEMETRY_PROM_URL="${TELEMETRY_PROM_URL:-http://127.0.0.1:9090}"
TELEMETRY_PROM_STEP="${TELEMETRY_PROM_STEP:-5s}"
TELEMETRY_HOST_INTERVAL="${TELEMETRY_HOST_INTERVAL:-1s}"

formal_profiler_pid=""
current_run_id=""
cleanup() {
  local rc=$?
  if [[ -n "${formal_profiler_pid}" ]] && kill -0 "${formal_profiler_pid}" 2>/dev/null; then
    kill -TERM "${formal_profiler_pid}" 2>/dev/null || true
    wait "${formal_profiler_pid}" 2>/dev/null || true
  fi
  telemetry_stop >/dev/null 2>&1 || true
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
[[ "${REPEATS}" =~ ^[1-9][0-9]*$ ]] || formal_fail "REPEATS must be positive"

mkdir -p bin "${RESULT_ROOT}"
GOCACHE="${GOCACHE:-/tmp/gpu-fleet-formal-go-cache}" go build -o bin/loadgen ./cmd/loadgen
GOCACHE="${GOCACHE:-/tmp/gpu-fleet-formal-go-cache}" go build -o bin/profiler ./cmd/profiler
kubectl delete pods -n "${NAMESPACE}" --all --ignore-not-found --wait=true >/dev/null
kubectl delete namespace exp2-filler --ignore-not-found --wait=true >/dev/null

matrix="${RESULT_ROOT}/${BATCH_ID}-matrix.csv"
[[ ! -e "${matrix}" ]] || formal_fail "matrix already exists: ${matrix}"
echo "batch_id,nodes,repeat,run_id,status,result_dir" >"${matrix}"

for nodes in ${NODE_COUNTS}; do
  echo "== Exp1: reset fleet to N=${nodes} =="
  formal_reset_nodes "${nodes}"
  echo "settle ${SETTLE_SECONDS}s"
  sleep "${SETTLE_SECONDS}"

  for repeat in $(seq 1 "${REPEATS}"); do
    current_run_id="${BATCH_ID}-n${nodes}-r${repeat}"
    result_dir="${RESULT_ROOT}/N${nodes}/${current_run_id}"
    [[ ! -e "${result_dir}" ]] || formal_fail "result directory exists: ${result_dir}"
    mkdir -p "${result_dir}"
    echo "== Exp1 N=${nodes} repeat=${repeat}/${REPEATS} =="

    telemetry_start \
      "${current_run_id}" exp1 default-scheduler "${result_dir}/pods" \
      "nodes=${nodes}" "scheduler=default-scheduler" "arrival=constant" \
      "qps=${TARGET_QPS}" "seed=${SEED}" "expected_pods=${EXPECTED_PODS}" \
      "client_qps=200" "client_burst=400" "settle_seconds=${SETTLE_SECONDS}" \
      "optimize=false" "namespace=${NAMESPACE}"

    bin/profiler \
      --namespace "${NAMESPACE}" \
      --label-selector "exp2.dev/run-id=${current_run_id}" \
      --submit-log "${result_dir}/submit.jsonl" \
      --out "${result_dir}/pods.csv" \
      --duration 90s --drain 60s \
      >"${result_dir}/profiler.log" 2>&1 &
    formal_profiler_pid=$!
    formal_wait_profiler "${formal_profiler_pid}" "${result_dir}/profiler.log"

    bin/loadgen \
      --namespace "${NAMESPACE}" \
      --scheduler-name default-scheduler \
      --run-id "${current_run_id}" \
      --arrival constant --qps "${TARGET_QPS}" --burst 50 \
      --client-qps 200 --client-burst 400 \
      --duration 90 --max-pods "${EXPECTED_PODS}" --gpu 1 --seed "${SEED}" \
      --out "${result_dir}/submit.jsonl" \
      >"${result_dir}/loadgen.log" 2>&1

    wait "${formal_profiler_pid}"
    formal_profiler_pid=""
    telemetry_stop
    telemetry_validate "${result_dir}/pods"

    submitted="$(wc -l <"${result_dir}/submit.jsonl" | tr -d ' ')"
    observed=$(( $(wc -l <"${result_dir}/pods.csv") - 1 ))
    censored="$(awk -F, 'NR>1 && $15=="true" {n++} END {print n+0}' "${result_dir}/pods.csv")"
    [[ "${submitted}" -eq "${EXPECTED_PODS}" ]] || formal_fail "submitted=${submitted}, want ${EXPECTED_PODS}"
    [[ "${observed}" -eq "${EXPECTED_PODS}" ]] || formal_fail "observed=${observed}, want ${EXPECTED_PODS}"
    [[ "${censored}" -eq 0 ]] || formal_fail "censored=${censored}, want 0"
    formal_validate_loadgen "${result_dir}/loadgen.log" "${EXPECTED_PODS}" "${TARGET_QPS}"

    echo "${BATCH_ID},${nodes},${repeat},${current_run_id},PASS,${result_dir}" >>"${matrix}"
    formal_delete_run_pods "${NAMESPACE}" "${current_run_id}"
    current_run_id=""
  done
done

echo "PASS: Exp1 matrix complete: ${matrix}"
echo "Analyze: python analysis/analyze_exp1.py --input ${RESULT_ROOT}"
