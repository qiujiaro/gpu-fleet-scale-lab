# Day 01 — Environment + KWOK simulated cluster + SLO awareness

- **Date:** 2026-07-22
- **Time spent:** ~2h
- **Planned deliverable:** A ~1000-node KWOK cluster can start/stop; Prometheus is wired up.
- **Core module to hand-write:** Environment scripts (may be vibe-coded).

## Goals for today

- [x] Install `kwokctl` / Docker and bring up a simulated control plane.
- [x] Scale the fake GP   U node pool to ~1000 nodes and confirm it is stable.
- [x] Wire Prometheus and confirm API server / scheduler metrics are scraped.

## What I did

Created a KWOK cluster using the Docker runtime:

```bash
kwokctl create cluster --name gpu-scale \
  --runtime docker \
  --prometheus-port 9090
```

Check whether this version requires Prometheus to be enabled explicitly:

```bash
kwokctl create cluster --help | grep -i prometheus
kwokctl get components --name gpu-scale
```


Created fake GPU Nodes from two files:

- `node-template.yaml` describes one fake Node: labels, topology, capacity, allocatable resources, and `Ready=True`.
- `spawn-nodes.sh N` renders the template N times, replaces the name/domain/zone placeholders, and pipes the generated YAML to `kubectl apply -f -`.

The template advertises each fake Node as:

```text
64 CPU, 512 GiB RAM, 256 Pod slots, 8 GPUs, Ready=True
```

These are real Kubernetes API objects stored through the API server, but no real machines, GPUs, kubelets, or containers exist.

## What I learned

### 1. What KWOK replaces
### 1. What KWOK replaces

KWOK retains the real Kubernetes control plane but replaces thousands of independent node-side kubelets with one centralized `kwok-controller`.

| Responsibility | Real Kubernetes cluster | KWOK cluster | What is no longer measured |
|---|---|---|---|
| Node status | Each Node runs a kubelet that reports conditions, capacity, and health | One `kwok-controller` updates fake Node status | Per-node kubelet CPU/memory, independent connections, retries, and failures |
| Node heartbeat | Each kubelet periodically renews its Node Lease | The central controller renews Leases for all fake Nodes | Real heartbeat jitter, network delay, disconnections, and kubelet restarts |
| Pod lifecycle | Kubelet coordinates Pod startup and reports status | KWOK directly advances Pod status through configured stages | Real startup work and node-side lifecycle latency |
| Container execution | Kubelet calls a CRI runtime such as containerd | No container is created | CRI, containerd, `runc`, namespaces, cgroups, and process startup |
| Image management | Node downloads, unpacks, caches, and garbage-collects images | Images are not pulled or validated | Registry latency, disk I/O, decompression, and `ImagePullBackOff` |
| Networking | CNI configures Pod networking; kube-proxy/eBPF programs the data plane | No real Pod network is created | CNI latency, routing, Service programming, throughput, and packet loss |
| Storage | Kubelet and CSI node plugins attach and mount volumes | No real volume operation occurs | Attach, mount, unmount, detach, filesystem, and storage I/O latency |
| GPU/device management | Device plugins, drivers, and runtimes expose and allocate physical GPUs | GPU capacity is only advertised in the Node object | Driver initialization, device allocation, CUDA startup, GPU execution, and NCCL traffic |
| Resource usage | Pods consume real CPU, memory, disk, network, and GPU resources | Resources exist only as scheduling metadata | Real contention, throttling, eviction, pressure, and hardware saturation |

The following control-plane behavior remains real:

| Preserved component/behavior | What KWOK still exercises |
|---|---|
| `kube-apiserver` | Authentication, validation, admission, API request processing, and watch delivery |
| `etcd` | Storage and retrieval of Node, Pod, Lease, and binding objects |
| `kube-scheduler` | Filtering, scoring, topology decisions, resource accounting, and Pod binding |
| `kube-controller-manager` | Reconciliation loops and reactions to API object changes |
| Kubernetes API objects | Real Node/Pod metadata, status, labels, events, and object churn |

Therefore, KWOK measures **control-plane scalability over simulated API objects**, not real node, container, network, storage, or GPU performance.

### 2. Kubernetes scalability SLOs

| SLI | Measurement | SLO |
|---|---|---:|
| Mutating API call latency | Per `(resource, verb)`; single-object writes; 5-minute P99 window | P99 ≤ 1 s |
| Read-only single-resource latency | Per `(resource, scope)` | P99 ≤ 1 s |
| Namespace/cluster LIST latency | Non-streaming list requests | P99 ≤ 30 s |
| Stateless Pod startup latency | Pod creation to all containers reported started and observed through watch | P99 ≤ 5 s |
| Watch latency | Database storage to event ready for all watchers; 5-minute P99 | WIP; no official numeric SLO |

Pod startup latency excludes image pulling and init-container execution. In KWOK these phases are not merely excluded from the metric—they do not happen at all.

`SLI` defines what is measured. `SLO` defines the target. `SLA` is a contractual commitment and may attach consequences to missing a target.

### 3. Why P99 and cluster-day matter

- An average can hide a small but operationally important set of very slow requests.
- At high request volume, even the slowest 1% represents many requests.
- Tail latency can cause timeouts, retries, queue buildup, and cascading control-plane pressure.
- A cluster-day/good-minutes view prevents long healthy periods from hiding shorter periods of severe degradation.

Short version: **P99 prevents fast requests from hiding slow requests; cluster-day prevents healthy periods from hiding bad periods.**

### 4. Fake GPU Node topology

- `topology.kubernetes.io/zone` is a standard, relatively large availability/failure domain.
- `topology.nvidia.com/nvlink-domain` is a custom, smaller high-speed interconnect grouping used by scheduling rules only when those rules explicitly reference the label.
- A training worker is a process/replica participating in a distributed training job, often packaged in a Pod. It is not the same as a Kubernetes worker Node.
- Distributed workers repeatedly exchange gradients/parameters. Keeping their Pods in the same fast network domain can reduce communication latency and prevent the slowest worker from delaying every synchronized training step.

KWOK tests whether the scheduler makes topology-aware placement decisions; it does not simulate actual NVLink, InfiniBand/RoCE, NCCL traffic, bandwidth, or latency.

The current script uses:

```bash
dom=$((i % 8))
zone=$((i % 3))
```

Because 8 and 3 are coprime, it cycles through 24 `(domain, zone)` combinations. This means one logical domain may span multiple zones, which may be unrealistic. A physical model would normally nest fabric domains inside zones.

### 5. YAML template vs. shell script

| File | Responsibility |
|---|---|
| `node-template.yaml` | Defines what one fake GPU Node looks like. |
| `spawn-nodes.sh N` | Chooses how many Nodes to create and assigns names, domains, and zones. |

Important fields:

- `labels` are metadata used by selectors and topology rules.
- `status.capacity` describes total resources.
- `status.allocatable` describes resources available to Pods and is used by the scheduler.
- `nvidia.com/gpu.count: "8"` is only a label.
- `status.allocatable["nvidia.com/gpu"]: "8"` is the schedulable extended resource.
- `Ready=True` makes the scheduler consider the fake Node healthy.
- `kwok.x-k8s.io/node: fake` marks the Node for KWOK management under the corresponding selector configuration.

## Key results / numbers

| Target Nodes | Starting Nodes | Total Nodes | Ready Nodes | Time until all Ready | Result |
|---:|---:|---:|---:|---:|---|
| 100 | 1000 | 1100 | 1100 | 0 s | Pass |
| 500 | 1100 | 1500 | 1500 | 1 s | Pass |
| 1000 | 1500 | 2000 | 2000 | 3 s | Pass |

Source: `gpu-fleet-scale-lab/smoke-scale/results/smoke-scale-results.csv` (generated by `gpu-fleet-scale-lab/smoke-scale/smoke-scale.sh`).

This is a feasibility smoke test, not a formal benchmark. The runs are incremental, have no repetitions, and are affected by warm caches and existing etcd state.

**Caveat — non-fresh cluster:** this sweep ran against the existing `gpu-scale` cluster, which already held 1000 custom `gpu-node-*` Nodes. Because `kwokctl scale node` *adds* its own `node-NNNNNN` Nodes on top of those, the total and ready counts are `1000 + target`, not the target itself, and each step only creates `target − previous_target` new Nodes. The sub-second-to-3-second readiness times therefore reflect small incremental additions on a warm control plane, not cold provisioning of the full target from zero. Host/Docker memory was not captured this run (TBD). A clean run on a fresh cluster (`CLUSTER=scale-test`, starting from 0) is needed for the isolated 0→100/500/1000 numbers.

Readiness must mean both:

1. the API contains exactly the target number of Nodes; and
2. all target Nodes report `Ready=True`.

Do not use only `kubectl wait ... node --all`: it can finish before Nodes that have not yet been created appear.

## Blockers & how I solved them

- **Prometheus UI unavailable:** verify that Prometheus is an enabled component, not merely that a port was supplied.
- **Wrong Node count:** do not mix `kwokctl scale node` Nodes with custom `gpu-node-*` Nodes.
- **Shell continuation failure:** the backslash must be the final character on the line; no trailing space after `\`.
- **Misleading readiness result:** poll both total Node count and Ready count.

## Open questions

- Which exact Prometheus-enable flag does the installed `kwokctl` version use?
- Should the synthetic topology model fabric domains as strictly contained within zones?
- Which scheduler topology policy will the later experiment test: affinity, topology spread, or a custom plugin?
- Which Node/Pod churn rates should the formal scale sweep use?

## Commit(s) / artifacts

- `config/kwok/node-template.yaml`
- `scripts/spawn-nodes.sh`
- Day 01 scale-smoke results (to be generated)


## Common commands

### Cluster lifecycle and context

```bash
kwokctl --version
kwokctl get clusters
kwokctl get components --name gpu-scale
kubectl config get-contexts
kubectl config use-context kwok-gpu-scale
kubectl cluster-info

kwokctl stop cluster --name gpu-scale
kwokctl start cluster --name gpu-scale
kwokctl delete cluster --name gpu-scale
```

Confirm exact syntax when unsure:

```bash
kwokctl --help
kwokctl create cluster --help
kwokctl port-forward --help
```

### Node creation and inspection

```bash
# Default KWOK Nodes; target total, not an increment
kwokctl scale node --name gpu-scale --replicas 100

# Custom GPU Nodes
./scripts/spawn-nodes.sh 100

kubectl get nodes
kubectl get nodes -o wide
kubectl get nodes --show-labels
kubectl get nodes -l type=kwok
kubectl describe node gpu-node-1
kubectl get node gpu-node-1 -o yaml

# Counts
kubectl get nodes --no-headers | wc -l
kubectl get nodes --no-headers | awk '$2 ~ /^Ready/ {n++} END {print n+0}'

# Inspect advertised capacity/allocatable resources
kubectl get node gpu-node-1 \
  -o jsonpath='{.status.capacity}{"\n"}{.status.allocatable}{"\n"}'
```

### Pod and scheduling inspection

```bash
kubectl get pods -A
kubectl get pods -A -o wide
kubectl describe pod POD_NAME -n NAMESPACE
kubectl get events -A --sort-by=.lastTimestamp

# Quick simulated Pod
kubectl run test-pod --image=nginx
kubectl get pod test-pod -o wide -w
```

In KWOK, a Pod may become `Running` without pulling the image or starting a real container.

### Remove custom Nodes

```bash
kubectl delete nodes -l type=kwok
```

This is destructive for the selected fake Node objects; verify the selector first:

```bash
kubectl get nodes -l type=kwok
```

### Prometheus

```bash
kwokctl get components --name gpu-scale
kwokctl port-forward prometheus --name gpu-scale
curl http://localhost:9090/-/ready
```

Open `http://localhost:9090`, then check **Status → Targets**.

Useful PromQL:

```promql
up
count by (job) (up)
sum by (verb) (rate(apiserver_request_total[5m]))
sum by (verb, code) (rate(apiserver_request_total{resource="nodes"}[5m]))
histogram_quantile(0.99,
  sum by (le, verb) (rate(apiserver_request_duration_seconds_bucket[5m]))
)
```

Metric names and labels can vary by Kubernetes version. Use Prometheus autocomplete and inspect **Status → Targets** if a query returns no data.

### Host memory

For Docker runtime:

```bash
docker stats --no-stream
docker stats --no-stream \
  --format 'table {{.Name}}\t{{.MemUsage}}\t{{.CPUPerc}}'
```

On macOS, also record Docker Desktop's total memory, memory pressure, and swap from Activity Monitor because per-container statistics omit part of the Docker Linux VM overhead.

## Additional knowledge not covered above

1. **Node objects are cluster-scoped.** They do not belong to a namespace.
2. **`capacity` is not consumption.** Scheduling uses allocatable capacity minus requested resources; KWOK does not consume the advertised hardware resources physically.
3. **GPU is an extended resource.** Pods normally request it under `limits`, and Kubernetes treats it as an integer resource that cannot be overcommitted.
4. **Labels do nothing by themselves.** A scheduler policy, affinity rule, topology constraint, controller, or plugin must reference a topology label.
5. **A default KWOK cluster normally uses a single etcd member.** It exercises etcd/API behavior but does not reproduce multi-member Raft replication, quorum latency, or leader failover.
6. **KWOK introduces its own simulation behavior.** One central controller is not traffic-equivalent to thousands of independent kubelets with separate connections, jitter, failures, retries, and backoff.
7. **Formal experiments need isolation.** Use a fresh cluster per scale point, fixed versions/configuration, repeated trials, controlled churn, warm-up periods, and percentile reporting.
8. **Control-plane success is not workload success.** A successful 1000-Node KWOK run shows that the simulated control plane can manage the API objects; it does not validate real container cold start, GPU initialization, storage, or network performance.

## References

- [KWOK Architecture](https://kwok.sigs.k8s.io/docs/design/architecture/)
- [KWOK Documentation](https://kwok.sigs.k8s.io/)
- [Kubernetes SIG Scalability SLOs](https://github.com/kubernetes/community/blob/main/sig-scalability/slos/slos.md)
