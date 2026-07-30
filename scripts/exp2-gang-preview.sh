#!/usr/bin/env bash
# Exp2 preview: four 1-GPU Pods compete for a deliberately undersized 3-GPU cluster.
# Expected result:
#   default-scheduler -> exactly 3/4 Pods acquire nodeName (partial occupancy)
#   topogang         -> 0/4 Pods acquire nodeName (whole group waits in PreFilter)
#
# Prerequisites:
#   - a disposable Kubernetes/KWOK cluster
#   - the custom scheduler running with config/scheduler/topogang-config.yaml
set -euo pipefail

NAMESPACE="${NAMESPACE:-exp2-preview}"
OBSERVE_SECONDS="${OBSERVE_SECONDS:-15}"
NODE_NAME="exp2-gpu-node"
DEFAULT_GROUP="exp2-default"
TOPOGANG_GROUP="exp2-topogang"

cleanup() {
  kubectl delete namespace "$NAMESPACE" --ignore-not-found --wait=false >/dev/null
  kubectl delete node "$NODE_NAME" --ignore-not-found >/dev/null
}
trap cleanup EXIT

cleanup
kubectl create namespace "$NAMESPACE" >/dev/null
kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Node
metadata:
  name: ${NODE_NAME}
  labels:
    type: kwok
    kwok.x-k8s.io/node: fake
    topology.nvidia.com/nvlink-domain: exp2-domain
  annotations:
    kwok.x-k8s.io/node: fake
status:
  capacity:
    cpu: "16"
    memory: 64Gi
    pods: "32"
    nvidia.com/gpu: "3"
  allocatable:
    cpu: "16"
    memory: 64Gi
    pods: "32"
    nvidia.com/gpu: "3"
  nodeInfo:
    kubeletVersion: fake
  conditions:
  - type: Ready
    status: "True"
    reason: KubeletReady
EOF

other_gpu_nodes="$(
  kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.allocatable.nvidia\.com/gpu}{"\n"}{end}' |
    awk -v expected="$NODE_NAME" '$1 != expected && $2+0 > 0 { print $1 }'
)"
if [[ -n "$other_gpu_nodes" ]]; then
  echo "FAIL: Exp2 needs an otherwise GPU-empty disposable cluster; found: ${other_gpu_nodes}" >&2
  exit 1
fi

make_pods() {
  local scheduler="$1"
  local group="$2"
  for i in 0 1 2 3; do
    kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  namespace: ${NAMESPACE}
  name: ${group}-${i}
  labels:
    experiment: exp2-preview
    variant: ${group}
    topogang.dev/pod-group: ${group}
    topogang.dev/min-member: "4"
spec:
  schedulerName: ${scheduler}
  restartPolicy: Never
  containers:
  - name: worker
    image: registry.k8s.io/pause:3.9
    resources:
      limits:
        nvidia.com/gpu: "1"
      requests:
        nvidia.com/gpu: "1"
EOF
  done
}

scheduled_count() {
  local group="$1"
  kubectl get pods -n "$NAMESPACE" -l "variant=${group}" \
    -o jsonpath='{range .items[?(@.spec.nodeName!="")]}{.metadata.name}{"\n"}{end}' |
    awk 'NF { count++ } END { print count+0 }'
}

echo "== default-scheduler: expect partial placement (3/4)"
make_pods default-scheduler "$DEFAULT_GROUP"
sleep "$OBSERVE_SECONDS"
default_scheduled="$(scheduled_count "$DEFAULT_GROUP")"
kubectl get pods -n "$NAMESPACE" -l "variant=${DEFAULT_GROUP}" -o wide
if [[ "$default_scheduled" -ne 3 ]]; then
  echo "FAIL: default scheduled ${default_scheduled}/4, want 3/4" >&2
  exit 1
fi

kubectl delete pods -n "$NAMESPACE" -l "variant=${DEFAULT_GROUP}" --wait=true >/dev/null

echo "== topogang: expect all-or-nothing wait (0/4)"
make_pods topogang "$TOPOGANG_GROUP"
sleep "$OBSERVE_SECONDS"
topogang_scheduled="$(scheduled_count "$TOPOGANG_GROUP")"
kubectl get pods -n "$NAMESPACE" -l "variant=${TOPOGANG_GROUP}" -o wide
kubectl get events -n "$NAMESPACE" --sort-by=.lastTimestamp |
  grep -E 'exp2-topogang|TopoGang|FailedScheduling' || true
if [[ "$topogang_scheduled" -ne 0 ]]; then
  echo "FAIL: topogang scheduled ${topogang_scheduled}/4, want 0/4" >&2
  exit 1
fi

echo "PASS: default=${default_scheduled}/4, topogang=${topogang_scheduled}/4"
