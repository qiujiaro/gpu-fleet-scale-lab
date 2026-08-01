#!/usr/bin/env bash
# Pre-fill a disposable cluster so expected experiment demand / remaining GPU capacity
# equals target rho. Prints the achieved values as one JSON object.
set -euo pipefail

TARGET_RHO=""
EXPECTED_DEMAND=""
NAMESPACE="exp2-filler"
SCHEDULER_NAME="default-scheduler"
RUN_ID="exp2"
TIMEOUT_SECONDS=60

usage() {
  echo "usage: $0 --target-rho R --expected-demand GPU [--namespace NS] [--scheduler-name NAME] [--run-id ID] [--timeout SECONDS]" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target-rho) TARGET_RHO="${2:?missing --target-rho value}"; shift 2 ;;
    --expected-demand) EXPECTED_DEMAND="${2:?missing --expected-demand value}"; shift 2 ;;
    --namespace) NAMESPACE="${2:?missing --namespace value}"; shift 2 ;;
    --scheduler-name) SCHEDULER_NAME="${2:?missing --scheduler-name value}"; shift 2 ;;
    --run-id) RUN_ID="${2:?missing --run-id value}"; shift 2 ;;
    --timeout) TIMEOUT_SECONDS="${2:?missing --timeout value}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "$TARGET_RHO" || -z "$EXPECTED_DEMAND" ]]; then
  usage
  exit 2
fi
if ! awk -v rho="$TARGET_RHO" -v demand="$EXPECTED_DEMAND" 'BEGIN { exit !(rho > 0 && demand > 0) }'; then
  echo "target rho and expected demand must be positive numbers" >&2
  exit 2
fi

TOTAL_GPU="$(
  kubectl get nodes -o jsonpath='{range .items[*]}{.status.allocatable.nvidia\.com/gpu}{"\n"}{end}' |
    awk '{ total += $1 + 0 } END { print total + 0 }'
)"
if [[ "$TOTAL_GPU" -le 0 ]]; then
  echo "cluster has no allocatable nvidia.com/gpu" >&2
  exit 1
fi

# rho = expected demand / remaining capacity. Round the desired remaining capacity up
# so prefill never makes achieved rho larger than requested solely due to rounding.
DESIRED_REMAINING="$(
  awk -v demand="$EXPECTED_DEMAND" -v rho="$TARGET_RHO" \
    'BEGIN { value=demand/rho; rounded=int(value); if (rounded < value) rounded++; print rounded }'
)"
FILLER_GPU=$(( TOTAL_GPU - DESIRED_REMAINING ))
if [[ "$FILLER_GPU" -lt 0 ]]; then
  FILLER_GPU=0
fi
if [[ "$FILLER_GPU" -ge "$TOTAL_GPU" ]]; then
  echo "computed filler (${FILLER_GPU}) leaves no GPU capacity" >&2
  exit 1
fi

kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
if [[ "$FILLER_GPU" -gt 0 ]]; then
  for i in $(seq 1 "$FILLER_GPU"); do
    kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  namespace: ${NAMESPACE}
  name: filler-${RUN_ID}-${i}
  labels:
    exp2.dev/role: filler
    exp2.dev/run-id: ${RUN_ID}
spec:
  schedulerName: ${SCHEDULER_NAME}
  restartPolicy: Never
  containers:
  - name: filler
    image: registry.k8s.io/pause:3.9
    resources:
      requests:
        nvidia.com/gpu: "1"
      limits:
        nvidia.com/gpu: "1"
EOF
  done
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
while true; do
  BOUND_GPU="$(
    kubectl get pods -n "$NAMESPACE" -l "exp2.dev/run-id=${RUN_ID},exp2.dev/role=filler" \
      -o jsonpath='{range .items[?(@.spec.nodeName!="")]}{.metadata.name}{"\n"}{end}' |
      awk 'NF { count++ } END { print count + 0 }'
  )"
  if [[ "$BOUND_GPU" -ge "$FILLER_GPU" ]]; then
    break
  fi
  if [[ $(date +%s) -ge "$deadline" ]]; then
    echo "timed out: only ${BOUND_GPU}/${FILLER_GPU} filler Pods bound" >&2
    exit 1
  fi
  sleep 1
done

REMAINING_GPU=$(( TOTAL_GPU - BOUND_GPU ))
ACHIEVED_RHO="$(awk -v demand="$EXPECTED_DEMAND" -v remaining="$REMAINING_GPU" 'BEGIN { printf "%.6f", demand/remaining }')"
printf '{"target_rho":%s,"achieved_rho":%s,"total_gpu":%d,"filler_gpu":%d,"remaining_gpu":%d,"expected_demand":%s}\n' \
  "$TARGET_RHO" "$ACHIEVED_RHO" "$TOTAL_GPU" "$BOUND_GPU" "$REMAINING_GPU" "$EXPECTED_DEMAND"
