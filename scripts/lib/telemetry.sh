#!/usr/bin/env bash
# Shared lifecycle for cmd/telemetry. Source this file after REPO_ROOT is set.

telemetry_pid=""
telemetry_prefix=""

telemetry_fail() {
  echo "FAIL: telemetry: $*" >&2
  return 1
}

telemetry_build() {
  [[ "${TELEMETRY_ENABLED:-true}" == "true" ]] || return 0
  local telemetry_bin="${TELEMETRY_BIN:-${REPO_ROOT}/bin/telemetry}"
  local telemetry_cache="${TELEMETRY_GOCACHE:-/tmp/gpu-fleet-telemetry-go-cache}"
  mkdir -p "$(dirname "${telemetry_bin}")"
  GOCACHE="${telemetry_cache}" go build -o "${telemetry_bin}" ./cmd/telemetry
  TELEMETRY_BIN="${telemetry_bin}"
}

# Usage:
# telemetry_start RUN_ID EXPERIMENT ARM OUT_PREFIX key=value ...
telemetry_start() {
  [[ "${TELEMETRY_ENABLED:-true}" == "true" ]] || return 0
  [[ -z "${telemetry_pid}" ]] || telemetry_fail "collector already running"

  local run_id="$1"
  local experiment="$2"
  local arm="$3"
  local out_prefix="$4"
  shift 4

  telemetry_build
  local prom_url="${TELEMETRY_PROM_URL-http://127.0.0.1:9090}"
  local host_interval="${TELEMETRY_HOST_INTERVAL:-1s}"
  local prom_step="${TELEMETRY_PROM_STEP:-5s}"
  local args=(
    --run-id "${run_id}"
    --experiment "${experiment}"
    --arm "${arm}"
    --out-prefix "${out_prefix}"
    --host-interval "${host_interval}"
    --repo-dir "${REPO_ROOT}"
  )
  if [[ -n "${prom_url}" ]]; then
    command -v curl >/dev/null || telemetry_fail "curl is required when Prometheus is enabled"
    curl --fail --silent --show-error "${prom_url%/}/api/v1/status/buildinfo" >/dev/null ||
      telemetry_fail "Prometheus is unavailable at ${prom_url}"
    args+=(--prom-url "${prom_url}" --prom-step "${prom_step}")
  fi
  local assignment
  for assignment in "$@"; do
    args+=(--meta "${assignment}")
  done

  mkdir -p "$(dirname "${out_prefix}")"
  telemetry_prefix="${out_prefix}"
  "${TELEMETRY_BIN}" "${args[@]}" >"${out_prefix}-telemetry.log" 2>&1 &
  telemetry_pid=$!

  local _
  for _ in $(seq 1 100); do
    if grep -q '"status": "running"' "${out_prefix}-meta.json" 2>/dev/null; then
      return 0
    fi
    if ! kill -0 "${telemetry_pid}" 2>/dev/null; then
      wait "${telemetry_pid}" || true
      telemetry_pid=""
      sed -n '1,120p' "${out_prefix}-telemetry.log" >&2 || true
      telemetry_fail "collector exited during startup"
    fi
    sleep 0.1
  done
  telemetry_stop || true
  telemetry_fail "collector did not become ready within 10 seconds"
}

telemetry_stop() {
  [[ "${TELEMETRY_ENABLED:-true}" == "true" ]] || return 0
  [[ -n "${telemetry_pid}" ]] || return 0

  local pid="${telemetry_pid}"
  telemetry_pid=""
  if kill -0 "${pid}" 2>/dev/null; then
    kill -TERM "${pid}" 2>/dev/null || true
  fi
  local rc=0
  wait "${pid}" || rc=$?
  if [[ "${rc}" -ne 0 ]]; then
    sed -n '1,160p' "${telemetry_prefix}-telemetry.log" >&2 || true
    telemetry_fail "collector exited with status ${rc}"
  fi
  grep -q '"status": "complete"' "${telemetry_prefix}-meta.json" ||
    telemetry_fail "meta.json does not report status=complete"
}

telemetry_validate() {
  [[ "${TELEMETRY_ENABLED:-true}" == "true" ]] || return 0
  local prefix="$1"
  [[ -s "${prefix}-meta.json" ]] || telemetry_fail "missing ${prefix}-meta.json"
  [[ -s "${prefix}-host.csv" ]] || telemetry_fail "missing ${prefix}-host.csv"
  if [[ -n "${TELEMETRY_PROM_URL-http://127.0.0.1:9090}" ]]; then
    [[ -s "${prefix}-prometheus.csv" ]] || telemetry_fail "missing ${prefix}-prometheus.csv"
    [[ -s "${prefix}-apiserver.csv" ]] || telemetry_fail "missing ${prefix}-apiserver.csv"
    [[ -s "${prefix}-pressure.csv" ]] || telemetry_fail "missing ${prefix}-pressure.csv"
  fi
}
