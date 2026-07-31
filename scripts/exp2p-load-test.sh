#!/usr/bin/env bash
# TopoGang stepped load test. Runs every QPS level repeatedly and writes one
# comparison-friendly summary.csv. A failed individual run does not stop the matrix.
set -euo pipefail

cd "$(dirname "$0")/.."

QPS_LEVELS="${QPS_LEVELS:-4 8 16 32 64}"
REPEATS="${REPEATS:-3}"
GANG_SIZE="${GANG_SIZE:-4}"
MAX_GANGS="${MAX_GANGS:-200}"
GPU_PER_POD="${GPU_PER_POD:-1}"
NAMESPACE="${NAMESPACE:-default}"
SCHEDULER_NAME="${SCHEDULER_NAME:-topogang}"
SCHEDULER_URL="${SCHEDULER_URL:-https://127.0.0.1:10260}"
BATCH_ID="${BATCH_ID:-exp2p-load-$(date +%Y%m%d-%H%M%S)-$$}"
RESULT_ROOT="${RESULT_ROOT:-experiments/exp2p-gang-profile/${BATCH_ID}}"
PAUSE_SECONDS="${PAUSE_SECONDS:-5}"
CLEANUP_OLD_PODS="${CLEANUP_OLD_PODS:-true}"
CLEANUP_PODS="${CLEANUP_PODS:-true}"
FAIL_ON_RUN_FAILURE="${FAIL_ON_RUN_FAILURE:-false}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ "${REPEATS}" =~ ^[1-9][0-9]*$ ]] || fail "REPEATS must be a positive integer"
[[ "${GANG_SIZE}" =~ ^[1-9][0-9]*$ ]] || fail "GANG_SIZE must be an integer > 1"
[[ "${GANG_SIZE}" -gt 1 ]] || fail "GANG_SIZE must be an integer > 1"
[[ "${MAX_GANGS}" =~ ^[1-9][0-9]*$ ]] || fail "MAX_GANGS must be a positive integer"
[[ "${PAUSE_SECONDS}" =~ ^[0-9]+$ ]] || fail "PAUSE_SECONDS must be a non-negative integer"
[[ "${BATCH_ID}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  fail "BATCH_ID must be a lowercase DNS-label-like value"
[[ ! -e "${RESULT_ROOT}" ]] ||
  fail "result root already exists: ${RESULT_ROOT}; choose a unique BATCH_ID"

for qps in ${QPS_LEVELS}; do
  awk -v qps="${qps}" 'BEGIN { exit !(qps > 0) }' ||
    fail "invalid QPS level: ${qps}"
done

mkdir -p "${RESULT_ROOT}"
summary="${RESULT_ROOT}/summary.csv"
batch_log="${RESULT_ROOT}/batch.log"

echo "batch_id,run_id,target_qps,repeat,status,expected_pods,submitted,observed,censored,censored_rate,scheduling_p50_ms,scheduling_p95_ms,scheduling_p99_ms,e2e_p50_ms,e2e_p95_ms,e2e_p99_ms,t_submit_p50_ms,t_submit_p95_ms,t_submit_p99_ms,t_group_ready_p50_ms,t_group_ready_p95_ms,t_group_ready_p99_ms,prefilter_calls,prefilter_nodes_avg,registry_lock_calls,registry_lock_wait_total_ms,registry_lock_wait_avg_us,permit_allowed,permit_wait_avg_ms,waiting_iterated_avg,gang_rejects,bind_calls,bind_avg_ms,result_dir" \
  >"${summary}"

metric_delta() {
  local before_file="$1"
  local after_file="$2"
  local metric_re="$3"
  awk -v re="${metric_re}" '
    FNR == NR && $1 !~ /^#/ {
      if ($1 ~ re) before[$1] = $2
      next
    }
    $1 !~ /^#/ && $1 ~ re {
      delta += $2 - before[$1]
    }
    END { printf "%.9f", delta+0 }
  ' "${before_file}" "${after_file}"
}

csv_phase_value() {
  local file="$1"
  local phase="$2"
  local column="$3"
  awk -F, -v phase="${phase}" -v column="${column}" '
    $1 == phase { print $column; found=1; exit }
    END { if (!found) print "" }
  ' "${file}"
}

append_run_summary() {
  local run_id="$1"
  local target_qps="$2"
  local repeat="$3"
  local status="$4"
  local run_dir="$5"
  local expected_pods=$(( GANG_SIZE * MAX_GANGS ))
  local submitted=0 observed=0 censored=0 censored_rate=""
  local scheduling_p50="" scheduling_p95="" scheduling_p99=""
  local e2e_p50="" e2e_p95="" e2e_p99=""
  local t_submit_p50="" t_submit_p95="" t_submit_p99=""
  local t_ready_p50="" t_ready_p95="" t_ready_p99=""
  local prefilter_calls=0 prefilter_nodes_sum=0 prefilter_nodes_avg=""
  local lock_calls=0 lock_wait_seconds=0 lock_wait_total_ms="" lock_wait_avg_us=""
  local permit_allowed=0 permit_wait_seconds=0 permit_wait_avg_ms=""
  local waiting_count=0 waiting_sum=0 waiting_avg=""
  local rejects=0 bind_calls=0 bind_seconds=0 bind_avg_ms=""

  [[ -f "${run_dir}/submit.jsonl" ]] &&
    submitted="$(awk 'END {print NR+0}' "${run_dir}/submit.jsonl")"
  if [[ -f "${run_dir}/pods.csv" ]]; then
    observed="$(awk 'NR > 1 {n++} END {print n+0}' "${run_dir}/pods.csv")"
    censored="$(awk -F, 'NR > 1 && $15 == "true" {n++} END {print n+0}' "${run_dir}/pods.csv")"
    censored_rate="$(awk -v n="${observed}" -v c="${censored}" \
      'BEGIN {if (n) printf "%.6f", c/n; else print ""}')"
  fi
  if [[ -f "${run_dir}/pods-summary.csv" ]]; then
    scheduling_p50="$(csv_phase_value "${run_dir}/pods-summary.csv" scheduling 6)"
    scheduling_p95="$(csv_phase_value "${run_dir}/pods-summary.csv" scheduling 7)"
    scheduling_p99="$(csv_phase_value "${run_dir}/pods-summary.csv" scheduling 8)"
    e2e_p50="$(csv_phase_value "${run_dir}/pods-summary.csv" end-to-end 6)"
    e2e_p95="$(csv_phase_value "${run_dir}/pods-summary.csv" end-to-end 7)"
    e2e_p99="$(csv_phase_value "${run_dir}/pods-summary.csv" end-to-end 8)"
  fi
  if [[ -f "${run_dir}/pods-group-summary.csv" ]]; then
    t_submit_p50="$(csv_phase_value "${run_dir}/pods-group-summary.csv" t_submit 6)"
    t_submit_p95="$(csv_phase_value "${run_dir}/pods-group-summary.csv" t_submit 7)"
    t_submit_p99="$(csv_phase_value "${run_dir}/pods-group-summary.csv" t_submit 8)"
    t_ready_p50="$(csv_phase_value "${run_dir}/pods-group-summary.csv" t_group_ready 6)"
    t_ready_p95="$(csv_phase_value "${run_dir}/pods-group-summary.csv" t_group_ready 7)"
    t_ready_p99="$(csv_phase_value "${run_dir}/pods-group-summary.csv" t_group_ready 8)"
  fi

  if [[ -f "${run_dir}/scheduler-before.metrics" &&
        -f "${run_dir}/scheduler-after.metrics" ]]; then
    prefilter_calls="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_prefilter_nodes_scanned_count$')"
    prefilter_nodes_sum="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_prefilter_nodes_scanned_sum$')"
    prefilter_nodes_avg="$(awk -v s="${prefilter_nodes_sum}" -v n="${prefilter_calls}" \
      'BEGIN {if (n) printf "%.3f", s/n; else print ""}')"

    lock_calls="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_registry_lock_wait_seconds_count$')"
    lock_wait_seconds="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_registry_lock_wait_seconds_sum$')"
    lock_wait_total_ms="$(awk -v s="${lock_wait_seconds}" 'BEGIN {printf "%.6f", s*1000}')"
    lock_wait_avg_us="$(awk -v s="${lock_wait_seconds}" -v n="${lock_calls}" \
      'BEGIN {if (n) printf "%.3f", s*1000000/n; else print ""}')"

    permit_allowed="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_podgroup_wait_seconds_count\\{outcome=\"allowed\"\\}$')"
    permit_wait_seconds="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_podgroup_wait_seconds_sum\\{outcome=\"allowed\"\\}$')"
    permit_wait_avg_ms="$(awk -v s="${permit_wait_seconds}" -v n="${permit_allowed}" \
      'BEGIN {if (n) printf "%.3f", s*1000/n; else print ""}')"

    waiting_count="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_waiting_pods_iterated_count$')"
    waiting_sum="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_waiting_pods_iterated_sum$')"
    waiting_avg="$(awk -v s="${waiting_sum}" -v n="${waiting_count}" \
      'BEGIN {if (n) printf "%.3f", s/n; else print ""}')"
    rejects="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^topogang_gang_reject_total')"

    bind_calls="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^scheduler_framework_extension_point_duration_seconds_count\\{extension_point=\"Bind\",profile=\"topogang\",status=\"Success\"\\}$')"
    bind_seconds="$(metric_delta "${run_dir}/scheduler-before.metrics" "${run_dir}/scheduler-after.metrics" \
      '^scheduler_framework_extension_point_duration_seconds_sum\\{extension_point=\"Bind\",profile=\"topogang\",status=\"Success\"\\}$')"
    bind_avg_ms="$(awk -v s="${bind_seconds}" -v n="${bind_calls}" \
      'BEGIN {if (n) printf "%.3f", s*1000/n; else print ""}')"
  fi

  echo "${BATCH_ID},${run_id},${target_qps},${repeat},${status},${expected_pods},${submitted},${observed},${censored},${censored_rate},${scheduling_p50},${scheduling_p95},${scheduling_p99},${e2e_p50},${e2e_p95},${e2e_p99},${t_submit_p50},${t_submit_p95},${t_submit_p99},${t_ready_p50},${t_ready_p95},${t_ready_p99},${prefilter_calls},${prefilter_nodes_avg},${lock_calls},${lock_wait_total_ms},${lock_wait_avg_us},${permit_allowed},${permit_wait_avg_ms},${waiting_avg},${rejects},${bind_calls},${bind_avg_ms},${run_dir}" \
    >>"${summary}"
}

total_runs=0
failed_runs=0
first_run=true

{
  echo "== Exp2-P stepped load test =="
  echo "batch_id=${BATCH_ID}"
  echo "qps_levels=${QPS_LEVELS} repeats=${REPEATS}"
  echo "gang_size=${GANG_SIZE} max_gangs=${MAX_GANGS}"
  echo "result_root=${RESULT_ROOT}"
} | tee "${batch_log}"

for qps in ${QPS_LEVELS}; do
  qps_id="$(sed 's/[^0-9a-zA-Z-]/-/g' <<<"${qps}" | tr '[:upper:]' '[:lower:]')"
  for repeat in $(seq 1 "${REPEATS}"); do
    total_runs=$(( total_runs + 1 ))
    run_id="${BATCH_ID}-q${qps_id}-r${repeat}"
    run_dir="${RESULT_ROOT}/${run_id}"

    echo "== run ${total_runs}: qps=${qps} repeat=${repeat}/${REPEATS} ==" | tee -a "${batch_log}"
    old_cleanup=false
    if [[ "${first_run}" == "true" ]]; then
      old_cleanup="${CLEANUP_OLD_PODS}"
      first_run=false
    fi

    set +e
    RUN_ID="${run_id}" \
      RESULT_DIR="${run_dir}" \
      TARGET_QPS="${qps}" \
      GANG_SIZE="${GANG_SIZE}" \
      MAX_GANGS="${MAX_GANGS}" \
      GPU_PER_POD="${GPU_PER_POD}" \
      NAMESPACE="${NAMESPACE}" \
      SCHEDULER_NAME="${SCHEDULER_NAME}" \
      SCHEDULER_URL="${SCHEDULER_URL}" \
      CLEANUP_OLD_PODS="${old_cleanup}" \
      CLEANUP_PODS="${CLEANUP_PODS}" \
      ./scripts/exp2p-smoke.sh 2>&1 | tee "${run_dir}.console.log"
    run_rc="${PIPESTATUS[0]}"
    set -e

    status="PASS"
    if [[ "${run_rc}" -ne 0 ]]; then
      status="FAIL"
      failed_runs=$(( failed_runs + 1 ))
    fi
    append_run_summary "${run_id}" "${qps}" "${repeat}" "${status}" "${run_dir}"
    echo "run_id=${run_id} status=${status} artifacts=${run_dir}" | tee -a "${batch_log}"

    if [[ "${PAUSE_SECONDS}" -gt 0 &&
          "${total_runs}" -lt $(( $(wc -w <<<"${QPS_LEVELS}") * REPEATS )) ]]; then
      echo "pause ${PAUSE_SECONDS}s before next run" | tee -a "${batch_log}"
      sleep "${PAUSE_SECONDS}"
    fi
  done
done

echo "== batch complete: runs=${total_runs} failed=${failed_runs} ==" | tee -a "${batch_log}"
echo "Summary: ${summary}" | tee -a "${batch_log}"

if [[ "${FAIL_ON_RUN_FAILURE}" == "true" && "${failed_runs}" -gt 0 ]]; then
  exit 1
fi
