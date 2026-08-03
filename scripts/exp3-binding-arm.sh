#!/usr/bin/env bash
# Run one Day 6/Exp3 arm against an already-running scheduler profile.
# Start cmd/scheduler separately with the matching baseline or optimized config.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"
# shellcheck source=scripts/lib/telemetry.sh
source "${REPO_ROOT}/scripts/lib/telemetry.sh"

ARM="${ARM:-baseline}"
NAMESPACE="${NAMESPACE:-default}"
TARGET_QPS="${TARGET_QPS:-25}"
DURATION_SECONDS="${DURATION_SECONDS:-20}"
SEED="${SEED:-42}"
RUN_ID="${RUN_ID:-exp3-${ARM}-$(date +%Y%m%d-%H%M%S)-$$}"
SCHEDULER_URL="${SCHEDULER_URL:-https://127.0.0.1:10260}"
RESULT_DIR="${RESULT_DIR:-experiments/exp3-burst/${ARM}/${RUN_ID}}"
CLEANUP_PODS="${CLEANUP_PODS:-true}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

case "${ARM}" in
  baseline) SCHEDULER_NAME="day6-baseline" ;;
  optimized) SCHEDULER_NAME="day6-optimized" ;;
  *) fail "ARM must be baseline or optimized" ;;
esac

awk -v qps="${TARGET_QPS}" 'BEGIN { exit !(qps > 0) }' || fail "TARGET_QPS must be > 0"
[[ "${DURATION_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail "DURATION_SECONDS must be > 0"
[[ "${CLEANUP_PODS}" == "true" || "${CLEANUP_PODS}" == "false" ]] ||
  fail "CLEANUP_PODS must be true or false"
[[ ! -e "${RESULT_DIR}" ]] || fail "result directory already exists: ${RESULT_DIR}"

for command in kubectl curl go; do
  command -v "${command}" >/dev/null || fail "${command} is required"
done
kubectl get --raw=/healthz >/dev/null || fail "Kubernetes API server is not healthy"
curl -ksSf "${SCHEDULER_URL}/healthz" >/dev/null ||
  fail "scheduler health check failed at ${SCHEDULER_URL}/healthz"

mkdir -p "${RESULT_DIR}"
tmp_dir="$(mktemp -d /tmp/gpu-lab-exp3.XXXXXX)"
profile_seconds=$(( DURATION_SECONDS + 30 ))
profiler_pid=""

cleanup() {
  rc=$?
  if [[ -n "${profiler_pid}" ]] && kill -0 "${profiler_pid}" 2>/dev/null; then
    kill "${profiler_pid}" 2>/dev/null || true
    wait "${profiler_pid}" 2>/dev/null || true
  fi
  if ! telemetry_stop; then
    [[ "${rc}" -ne 0 ]] || rc=1
  fi
  if [[ "${CLEANUP_PODS}" == "true" ]]; then
    kubectl delete pods -n "${NAMESPACE}" -l "exp2.dev/run-id=${RUN_ID}" \
      --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
  rm -rf "${tmp_dir}"
  exit "${rc}"
}
trap cleanup EXIT INT TERM

GOCACHE="${GOCACHE:-/tmp/gpu-fleet-day6-go-cache}" go build -o "${tmp_dir}/loadgen" ./cmd/loadgen
GOCACHE="${GOCACHE:-/tmp/gpu-fleet-day6-go-cache}" go build -o "${tmp_dir}/profiler" ./cmd/profiler

curl -ksSf "${SCHEDULER_URL}/metrics" >"${RESULT_DIR}/scheduler-before.metrics"
ready_nodes="$(
  kubectl get nodes --no-headers |
    awk '$2 == "Ready" { count++ } END { print count+0 }'
)"
telemetry_start \
  "${RUN_ID}" exp3 "${ARM}" "${RESULT_DIR}/pods" \
  "nodes=${ready_nodes}" \
  "scheduler=${SCHEDULER_NAME}" \
  "optimize=$([[ "${ARM}" == "optimized" ]] && echo true || echo false)" \
  "qps=${TARGET_QPS}" \
  "seed=${SEED}" \
  "workload_duration_seconds=${DURATION_SECONDS}" \
  "namespace=${NAMESPACE}" \
  "arrival=constant" \
  "simulated_cold_start=true"
"${tmp_dir}/profiler" \
  --namespace "${NAMESPACE}" \
  --label-selector "exp2.dev/run-id=${RUN_ID}" \
  --submit-log "${RESULT_DIR}/submit.jsonl" \
  --out "${RESULT_DIR}/pods.csv" \
  --duration "${profile_seconds}s" \
  --drain 10s >"${RESULT_DIR}/profiler.log" 2>&1 &
profiler_pid=$!

for _ in $(seq 1 100); do
  grep -q 'profiler: watching' "${RESULT_DIR}/profiler.log" && break
  kill -0 "${profiler_pid}" 2>/dev/null || fail "profiler exited before watch startup"
  sleep 0.1
done
grep -q 'profiler: watching' "${RESULT_DIR}/profiler.log" || fail "profiler watch startup timed out"

"${tmp_dir}/loadgen" \
  --namespace "${NAMESPACE}" \
  --scheduler-name "${SCHEDULER_NAME}" \
  --run-id "${RUN_ID}" \
  --simulated-cold-start \
  --arrival constant \
  --qps "${TARGET_QPS}" \
  --burst 100 \
  --duration "${DURATION_SECONDS}" \
  --seed "${SEED}" \
  --out "${RESULT_DIR}/submit.jsonl" >"${RESULT_DIR}/loadgen.log" 2>&1

wait "${profiler_pid}"
profiler_pid=""
curl -ksSf "${SCHEDULER_URL}/metrics" >"${RESULT_DIR}/scheduler-after.metrics"
telemetry_stop

for file in submit.jsonl pods.csv pods-summary.csv profiler.log loadgen.log \
  scheduler-before.metrics scheduler-after.metrics; do
  [[ -s "${RESULT_DIR}/${file}" ]] || fail "missing or empty artifact: ${file}"
done
telemetry_validate "${RESULT_DIR}/pods"

submitted="$(wc -l <"${RESULT_DIR}/submit.jsonl" | tr -d ' ')"
observed=$(( $(wc -l <"${RESULT_DIR}/pods.csv") - 1 ))
[[ "${submitted}" -eq "${observed}" ]] || fail "submitted=${submitted}, observed=${observed}"
awk -F, 'NR > 1 && $15 != "false" { bad++ } END { exit bad != 0 }' "${RESULT_DIR}/pods.csv" ||
  fail "one or more Pod samples are censored"

echo "PASS: arm=${ARM} submitted=${submitted} observed=${observed}"
echo "Artifacts: ${RESULT_DIR}"
