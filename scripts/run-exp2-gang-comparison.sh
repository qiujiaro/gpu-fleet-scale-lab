#!/usr/bin/env bash
# Guide Exp2: paired default-scheduler vs TopoGang under fixed 98.4375% occupancy.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"
# shellcheck source=scripts/lib/formal.sh
source "${REPO_ROOT}/scripts/lib/formal.sh"
# shellcheck source=scripts/lib/telemetry.sh
source "${REPO_ROOT}/scripts/lib/telemetry.sh"

NODES="${NODES:-500}"
SEEDS="${SEEDS:-42 43 44}"
GANGS="${GANGS:-100}"
GANG_SIZE="${GANG_SIZE:-8}"
TARGET_QPS="${TARGET_QPS:-40}"
CUTOFF_SECONDS="${CUTOFF_SECONDS:-60}"
SETTLE_SECONDS="${SETTLE_SECONDS:-60}"
NAMESPACE="${NAMESPACE:-default}"
FILLER_NAMESPACE="${FILLER_NAMESPACE:-exp2-filler}"
BATCH_ID="${BATCH_ID:-exp2-gang-$(date +%Y%m%d-%H%M%S)-$$}"
RESULT_ROOT="${RESULT_ROOT:-experiments/exp2-gang}"
SCHEDULER_PORT="${SCHEDULER_PORT:-10260}"
SCHEDULER_URL="${SCHEDULER_URL:-https://127.0.0.1:${SCHEDULER_PORT}}"
SCHEDULER_BIN="${SCHEDULER_BIN:-${REPO_ROOT}/bin/scheduler}"
KUBECONFIG_PATH="${KUBECONFIG_PATH:-${KUBECONFIG:-${HOME}/.kube/config}}"
TELEMETRY_PROM_URL="${TELEMETRY_PROM_URL:-http://127.0.0.1:9090}"
TELEMETRY_PROM_STEP="${TELEMETRY_PROM_STEP:-5s}"
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
  kubectl delete namespace "${FILLER_NAMESPACE}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
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

echo "== Exp2: reset fleet to ${NODES} Nodes =="
formal_reset_nodes "${NODES}"
sleep "${SETTLE_SECONDS}"

echo "== Exp2: create one 63-GPU filler Pod per Node =="
kubectl delete namespace "${FILLER_NAMESPACE}" --ignore-not-found --wait=true >/dev/null
kubectl create namespace "${FILLER_NAMESPACE}" >/dev/null
{
  for i in $(seq 1 "${NODES}"); do
    cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  namespace: ${FILLER_NAMESPACE}
  name: filler-${i}
  labels:
    gpu-lab/role: exp2-filler
spec:
  schedulerName: default-scheduler
  restartPolicy: Never
  containers:
  - name: filler
    image: registry.k8s.io/pause:3.9
    resources:
      requests:
        nvidia.com/gpu: "63"
      limits:
        nvidia.com/gpu: "63"
---
EOF
  done
} | kubectl apply -f - >/dev/null

deadline=$(( $(date +%s) + 300 ))
while true; do
  bound="$(kubectl get pods -n "${FILLER_NAMESPACE}" -l gpu-lab/role=exp2-filler \
    -o jsonpath='{range .items[?(@.spec.nodeName!="")]}{.metadata.name}{"\n"}{end}' |
    awk 'NF {count++} END {print count+0}')"
  [[ "${bound}" -eq "${NODES}" ]] && break
  [[ $(date +%s) -lt "${deadline}" ]] || formal_fail "only ${bound}/${NODES} filler Pods bound"
  sleep 1
done

matrix="${RESULT_ROOT}/${BATCH_ID}-matrix.csv"
echo "batch_id,seed,order,arm,run_id,status,result_dir" >"${matrix}"

run_arm() {
  local arm="$1"
  local seed="$2"
  local order="$3"
  local scheduler_name result_dir
  scheduler_name="default-scheduler"
  [[ "${arm}" == "topogang" ]] && scheduler_name="topogang"
  current_run_id="${BATCH_ID}-s${seed}-${arm}"
  result_dir="${RESULT_ROOT}/${arm}/${current_run_id}"
  [[ ! -e "${result_dir}" ]] || formal_fail "result directory exists: ${result_dir}"
  mkdir -p "${result_dir}"

  if [[ "${arm}" == "topogang" ]]; then
    formal_start_scheduler "${REPO_ROOT}/config/scheduler/topogang-config.yaml" "${result_dir}/scheduler.log"
    curl -ksSf "${SCHEDULER_URL}/metrics" >"${result_dir}/scheduler-before.metrics"
  fi

  telemetry_start \
    "${current_run_id}" exp2 "${arm}" "${result_dir}/pods" \
    "nodes=${NODES}" "scheduler=${scheduler_name}" "arrival=poisson" \
    "qps=${TARGET_QPS}" "seed=${seed}" "expected_pods=$(( GANGS * GANG_SIZE ))" \
    "gang_size=${GANG_SIZE}" "gangs=${GANGS}" "client_qps=200" "client_burst=400" \
    "gpu_occupancy=0.984375" "cutoff_seconds=${CUTOFF_SECONDS}" "namespace=${NAMESPACE}"

  bin/profiler \
    --namespace "${NAMESPACE}" \
    --label-selector "exp2.dev/run-id=${current_run_id}" \
    --submit-log "${result_dir}/submit.jsonl" \
    --out "${result_dir}/pods.csv" \
    --group-out "${result_dir}/pods-groups.csv" \
    --group-summary-out "${result_dir}/pods-group-summary.csv" \
    >"${result_dir}/profiler.log" 2>&1 &
  formal_profiler_pid=$!
  formal_wait_profiler "${formal_profiler_pid}" "${result_dir}/profiler.log"

  bin/loadgen \
    --namespace "${NAMESPACE}" --scheduler-name "${scheduler_name}" \
    --run-id "${current_run_id}" --arrival poisson \
    --qps "${TARGET_QPS}" --burst 80 --client-qps 200 --client-burst 400 \
    --duration 90 --gang-size "${GANG_SIZE}" --max-gangs "${GANGS}" \
    --gpu 1 --seed "${seed}" --out "${result_dir}/submit.jsonl" \
    >"${result_dir}/loadgen.log" 2>&1

  sleep "${CUTOFF_SECONDS}"
  kill -TERM "${formal_profiler_pid}"
  wait "${formal_profiler_pid}"
  formal_profiler_pid=""
  if [[ "${arm}" == "topogang" ]]; then
    curl -ksSf "${SCHEDULER_URL}/metrics" >"${result_dir}/scheduler-after.metrics"
  fi
  telemetry_stop
  telemetry_validate "${result_dir}/pods"

  submitted="$(wc -l <"${result_dir}/submit.jsonl" | tr -d ' ')"
  observed=$(( $(wc -l <"${result_dir}/pods.csv") - 1 ))
  [[ "${submitted}" -eq $(( GANGS * GANG_SIZE )) ]] || formal_fail "submitted=${submitted}"
  [[ "${observed}" -eq "${submitted}" ]] || formal_fail "observed=${observed}, submitted=${submitted}"
  formal_validate_loadgen "${result_dir}/loadgen.log" "$(( GANGS * GANG_SIZE ))" "${TARGET_QPS}"
  echo "${BATCH_ID},${seed},${order},${arm},${current_run_id},PASS,${result_dir}" >>"${matrix}"

  formal_delete_run_pods "${NAMESPACE}" "${current_run_id}"
  current_run_id=""
  formal_stop_scheduler
}

repeat=0
for seed in ${SEEDS}; do
  repeat=$(( repeat + 1 ))
  if (( repeat % 2 == 1 )); then
    order="default-topogang"
    run_arm default "${seed}" "${order}"
    run_arm topogang "${seed}" "${order}"
  else
    order="topogang-default"
    run_arm topogang "${seed}" "${order}"
    run_arm default "${seed}" "${order}"
  fi
done

kubectl delete namespace "${FILLER_NAMESPACE}" --ignore-not-found --wait=true >/dev/null
echo "PASS: Exp2 paired matrix complete: ${matrix}"
echo "Figures: python analysis/plot.py --experiments experiments --out analysis/figures"
