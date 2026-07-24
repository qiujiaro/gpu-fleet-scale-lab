#!/usr/bin/env bash
# Calibrate the load generator before Exp3: prove the client can sustain 50 QPS
# for 60 seconds against a 1000-node cluster without receiving HTTP 429s.
set -euo pipefail

cd "$(dirname "$0")/.."

EXPECTED_NODES="${EXPECTED_NODES:-1000}"
TARGET_QPS="${TARGET_QPS:-50}"
DURATION_SEC="${DURATION_SEC:-60}"
MIN_QPS_RATIO="${MIN_QPS_RATIO:-0.95}"
RESULT_DIR="${RESULT_DIR:-experiments/day2-client-preflight}"

mkdir -p "${RESULT_DIR}" bin

node_rows="$(kubectl get nodes --no-headers)"
node_count="$(awk 'NF {count++} END {print count+0}' <<<"${node_rows}")"
ready_count="$(awk '$2 == "Ready" {count++} END {print count+0}' <<<"${node_rows}")"
if [[ "${node_count}" -ne "${EXPECTED_NODES}" || "${ready_count}" -ne "${EXPECTED_NODES}" ]]; then
  echo "FAIL: expected ${EXPECTED_NODES}/${EXPECTED_NODES} Ready nodes, got ${ready_count}/${node_count}" >&2
  exit 1
fi

go build -o bin/loadgen ./cmd/loadgen

jsonl="${RESULT_DIR}/run.jsonl"
log_file="${RESULT_DIR}/loadgen.log"
summary="${RESULT_DIR}/summary.csv"

set +e
bin/loadgen \
  --arrival constant \
  --qps "${TARGET_QPS}" \
  --burst "${TARGET_QPS}" \
  --duration "${DURATION_SEC}" \
  --gpu 1 \
  --out "${jsonl}" \
  2>&1 | tee "${log_file}"
loadgen_status="${PIPESTATUS[0]}"
set -e
if [[ "${loadgen_status}" -ne 0 ]]; then
  echo "FAIL: loadgen exited with status ${loadgen_status}" >&2
  exit "${loadgen_status}"
fi

stats_line="$(sed -n 's/.*loadgen: attempted=/attempted=/p' "${log_file}" | tail -n 1)"
if [[ -z "${stats_line}" ]]; then
  echo "FAIL: loadgen statistics were not found in ${log_file}" >&2
  exit 1
fi

attempted="$(sed -n 's/.*attempted=\([0-9][0-9]*\).*/\1/p' <<<"${stats_line}")"
succeeded="$(sed -n 's/.*succeeded=\([0-9][0-9]*\).*/\1/p' <<<"${stats_line}")"
failed="$(sed -n 's/.*failed=\([0-9][0-9]*\).*/\1/p' <<<"${stats_line}")"
rate_limited="$(sed -n 's/.*rate-limited=\([0-9][0-9]*\).*/\1/p' <<<"${stats_line}")"
actual_qps="$(awk -v n="${succeeded}" -v seconds="${DURATION_SEC}" 'BEGIN {printf "%.2f", n/seconds}')"
minimum_qps="$(awk -v target="${TARGET_QPS}" -v ratio="${MIN_QPS_RATIO}" 'BEGIN {printf "%.2f", target*ratio}')"

result="PASS"
if [[ "${rate_limited}" -ne 0 || "${failed}" -ne 0 ]]; then
  result="FAIL"
fi
if ! awk -v actual="${actual_qps}" -v minimum="${minimum_qps}" 'BEGIN {exit !(actual >= minimum)}'; then
  result="FAIL"
fi

{
  echo "result,nodes,ready_nodes,target_qps,duration_sec,attempted,succeeded,failed,rate_limited,actual_qps,min_required_qps"
  echo "${result},${node_count},${ready_count},${TARGET_QPS},${DURATION_SEC},${attempted},${succeeded},${failed},${rate_limited},${actual_qps},${minimum_qps}"
} >"${summary}"

echo "${result}: nodes=${ready_count}/${node_count} target_qps=${TARGET_QPS} actual_qps=${actual_qps} 429=${rate_limited} failed=${failed}"
echo "Artifacts: ${jsonl} ${log_file} ${summary}"

[[ "${result}" == "PASS" ]]
