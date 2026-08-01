#!/usr/bin/env bash
# End-to-end TopoGang profiling smoke.
#
# Prerequisites:
#   - current kubectl context points at a disposable KWOK cluster with Ready GPU nodes
#   - the TopoGang scheduler is already running and exposes /healthz and /metrics
#
# Useful overrides:
#   RUN_ID=smoke-local GANG_SIZE=4 MAX_GANGS=20 TARGET_QPS=4 \
#   CLEANUP_OLD_PODS=false CLEANUP_PODS=false ./scripts/exp2-topogang-smoke.sh
set -euo pipefail

cd "$(dirname "$0")/.."

RUN_ID="${RUN_ID:-exp2-smoke-$(date +%Y%m%d-%H%M%S)-$$}"
NAMESPACE="${NAMESPACE:-default}"
GANG_SIZE="${GANG_SIZE:-4}"
MAX_GANGS="${MAX_GANGS:-20}"
TARGET_QPS="${TARGET_QPS:-4}"
GPU_PER_POD="${GPU_PER_POD:-1}"
SCHEDULER_NAME="${SCHEDULER_NAME:-topogang}"
SCHEDULER_URL="${SCHEDULER_URL:-https://127.0.0.1:10260}"
CLEANUP_OLD_PODS="${CLEANUP_OLD_PODS:-true}"
CLEANUP_PODS="${CLEANUP_PODS:-true}"
RESULT_DIR="${RESULT_DIR:-experiments/exp2-topogang/controlled-load/local/${RUN_ID}}"

EXPECTED_PODS=$(( GANG_SIZE * MAX_GANGS ))
LOAD_SECONDS="$(
  awk -v pods="${EXPECTED_PODS}" -v qps="${TARGET_QPS}" \
    'BEGIN { seconds=int(pods/qps); if (seconds*qps < pods) seconds++; print seconds+10 }'
)"
PROFILE_SECONDS=$(( LOAD_SECONDS + 5 ))
PROFILE_DRAIN_SECONDS=10

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ "${GANG_SIZE}" -gt 1 ]] || fail "GANG_SIZE must be > 1"
[[ "${MAX_GANGS}" -gt 0 ]] || fail "MAX_GANGS must be > 0"
awk -v qps="${TARGET_QPS}" 'BEGIN { exit !(qps > 0) }' ||
  fail "TARGET_QPS must be > 0"
[[ "${RUN_ID}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  fail "RUN_ID must be a lowercase DNS-label-like value"
[[ "${CLEANUP_OLD_PODS}" == "true" || "${CLEANUP_OLD_PODS}" == "false" ]] ||
  fail "CLEANUP_OLD_PODS must be true or false"
[[ "${CLEANUP_PODS}" == "true" || "${CLEANUP_PODS}" == "false" ]] ||
  fail "CLEANUP_PODS must be true or false"
[[ ! -e "${RESULT_DIR}" ]] ||
  fail "result directory already exists: ${RESULT_DIR}; choose a unique RUN_ID"

command -v kubectl >/dev/null || fail "kubectl is required"
command -v curl >/dev/null || fail "curl is required"
command -v go >/dev/null || fail "go is required"

kubectl get --raw=/healthz >/dev/null ||
  fail "current Kubernetes API server is not healthy"
ready_nodes="$(
  kubectl get nodes --no-headers |
    awk '$2 == "Ready" { count++ } END { print count+0 }'
)"
[[ "${ready_nodes}" -gt 0 ]] || fail "no Ready nodes in the current cluster"

curl -ksSf "${SCHEDULER_URL}/healthz" >/dev/null ||
  fail "TopoGang scheduler health check failed at ${SCHEDULER_URL}/healthz"
curl -ksSf "${SCHEDULER_URL}/metrics" >/dev/null ||
  fail "TopoGang scheduler metrics are unavailable at ${SCHEDULER_URL}/metrics"

mkdir -p "${RESULT_DIR}"
tmp_dir="$(mktemp -d /tmp/exp2-smoke.XXXXXX)"
smoke_go_cache="${SMOKE_GOCACHE:-/tmp/gpu-fleet-go-cache}"
profiler_pid=""

cleanup() {
  rc=$?
  if [[ -n "${profiler_pid}" ]] && kill -0 "${profiler_pid}" 2>/dev/null; then
    kill "${profiler_pid}" 2>/dev/null || true
    wait "${profiler_pid}" 2>/dev/null || true
  fi
  if [[ "${CLEANUP_PODS}" == "true" ]]; then
    kubectl delete pods -n "${NAMESPACE}" \
      -l "exp2.dev/run-id=${RUN_ID}" \
      --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
  rm -rf "${tmp_dir}"
  exit "${rc}"
}
trap cleanup EXIT INT TERM

echo "== Exp2 controlled-load smoke =="
echo "run_id=${RUN_ID} namespace=${NAMESPACE} nodes=${ready_nodes}"
echo "gang_size=${GANG_SIZE} max_gangs=${MAX_GANGS} expected_pods=${EXPECTED_PODS} qps=${TARGET_QPS}"
echo "result_dir=${RESULT_DIR}"

if [[ "${CLEANUP_OLD_PODS}" == "true" ]]; then
  old_pod_count="$(
    kubectl get pods -n "${NAMESPACE}" \
      -l "exp2.dev/run-id" \
      --no-headers 2>/dev/null |
      awk 'END { print NR+0 }'
  )"
  echo "== delete ${old_pod_count} old experiment Pods =="
  if [[ "${old_pod_count}" -gt 0 ]]; then
    kubectl delete pods -n "${NAMESPACE}" \
      -l "exp2.dev/run-id" \
      --ignore-not-found --wait=true
  fi
else
  echo "== keep old experiment Pods (CLEANUP_OLD_PODS=false) =="
fi

echo "== build binaries =="
GOCACHE="${smoke_go_cache}" \
  go build -o "${tmp_dir}/profiler" ./cmd/profiler
GOCACHE="${smoke_go_cache}" \
  go build -o "${tmp_dir}/loadgen" ./cmd/loadgen

echo "== capture metrics before =="
curl -ksSf "${SCHEDULER_URL}/metrics" >"${RESULT_DIR}/scheduler-before.metrics"

echo "== start profiler =="
"${tmp_dir}/profiler" \
  --namespace "${NAMESPACE}" \
  --label-selector "exp2.dev/run-id=${RUN_ID}" \
  --submit-log "${RESULT_DIR}/submit.jsonl" \
  --out "${RESULT_DIR}/pods.csv" \
  --group-out "${RESULT_DIR}/groups.csv" \
  --duration "${PROFILE_SECONDS}s" \
  --drain "${PROFILE_DRAIN_SECONDS}s" \
  >"${RESULT_DIR}/profiler.log" 2>&1 &
profiler_pid=$!

for _ in $(seq 1 100); do
  if grep -q 'profiler: watching' "${RESULT_DIR}/profiler.log"; then
    break
  fi
  kill -0 "${profiler_pid}" 2>/dev/null ||
    fail "profiler exited before its watch started; see ${RESULT_DIR}/profiler.log"
  sleep 0.1
done
grep -q 'profiler: watching' "${RESULT_DIR}/profiler.log" ||
  fail "profiler watch did not start within 10 seconds"

echo "== submit ${MAX_GANGS} complete gangs =="
"${tmp_dir}/loadgen" \
  --namespace "${NAMESPACE}" \
  --scheduler-name "${SCHEDULER_NAME}" \
  --gang-size "${GANG_SIZE}" \
  --max-gangs "${MAX_GANGS}" \
  --run-id "${RUN_ID}" \
  --arrival constant \
  --qps "${TARGET_QPS}" \
  --duration "${LOAD_SECONDS}" \
  --gpu "${GPU_PER_POD}" \
  --out "${RESULT_DIR}/submit.jsonl" \
  >"${RESULT_DIR}/loadgen.log" 2>&1

echo "== wait for profiler =="
wait "${profiler_pid}"
profiler_pid=""

echo "== capture metrics after =="
curl -ksSf "${SCHEDULER_URL}/metrics" >"${RESULT_DIR}/scheduler-after.metrics"

echo "== validate artifacts =="
for file in \
  submit.jsonl pods.csv pods-summary.csv groups.csv pods-group-summary.csv \
  profiler.log loadgen.log scheduler-before.metrics scheduler-after.metrics
do
  [[ -s "${RESULT_DIR}/${file}" ]] || fail "missing or empty artifact: ${file}"
done

submit_count="$(wc -l <"${RESULT_DIR}/submit.jsonl" | tr -d ' ')"
pod_count=$(( $(wc -l <"${RESULT_DIR}/pods.csv") - 1 ))
group_count=$(( $(wc -l <"${RESULT_DIR}/groups.csv") - 1 ))
[[ "${submit_count}" -eq "${EXPECTED_PODS}" ]] ||
  fail "submit rows=${submit_count}, want ${EXPECTED_PODS}"
[[ "${pod_count}" -eq "${EXPECTED_PODS}" ]] ||
  fail "pod rows=${pod_count}, want ${EXPECTED_PODS}"
[[ "${group_count}" -eq "${MAX_GANGS}" ]] ||
  fail "group rows=${group_count}, want ${MAX_GANGS}"

awk -F, -v size="${GANG_SIZE}" '
  NR > 1 && ($2 != size || $3 != size || $14 != "false") { bad++ }
  END { exit bad != 0 }
' "${RESULT_DIR}/groups.csv" ||
  fail "one or more gangs are incomplete or censored"

awk -F, '
  NR > 1 && ($10 == "" || $11 == "" || $11+0 < $10+0) { bad++ }
  END { exit bad != 0 }
' "${RESULT_DIR}/groups.csv" ||
  fail "one or more gangs violate t_group_ready >= t_submit"

awk -F, '
  NR > 1 && ($15 != "false" || $11+0 < 0 || $12+0 < 0 || $13+0 < 0 || $14+0 < 0) { bad++ }
  END { exit bad != 0 }
' "${RESULT_DIR}/pods.csv" ||
  fail "one or more Pods are censored or have a negative latency"

grep -q "submitted=${EXPECTED_PODS} matched=${EXPECTED_PODS} unobserved=0 unsubmitted=0 censored=0" \
  "${RESULT_DIR}/profiler.log" ||
  fail "profiler join accounting did not close; see profiler.log"
grep -q "succeeded=${EXPECTED_PODS} failed=0 rate-limited=0" \
  "${RESULT_DIR}/loadgen.log" ||
  fail "loadgen validity check failed; see loadgen.log"

echo "== summaries =="
cat "${RESULT_DIR}/pods-group-summary.csv"
cat "${RESULT_DIR}/pods-summary.csv"
echo "PASS: ${MAX_GANGS} complete gangs, ${EXPECTED_PODS} matched Pods, no censoring or negative latency"
echo "Artifacts: ${RESULT_DIR}"
if [[ "${CLEANUP_PODS}" == "true" ]]; then
  echo "Pods from run ${RUN_ID} will be deleted on exit (set CLEANUP_PODS=false to keep them)."
fi
