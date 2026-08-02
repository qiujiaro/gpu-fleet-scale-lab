# Day 06 — Simulated cold start + coalesced binding

- **Date:** 2026-07-__
- **Time spent:** ~4h
- **Planned deliverable:** Configurable simulated cold start and a baseline versus
  optimized binding experiment.
- **Core module to hand-write:** The coalescing queue, its state machine, and its
  concurrency/backpressure policy.
- **Status:** Vibe-coded plumbing and the hand-written, race-tested batcher are complete;
  the live KWOK and baseline/optimized comparison remain.

## First: define the claim honestly

Kubernetes does not expose a multi-Pod binding API. A scheduler `BindPlugin` is invoked
for one Pod and ultimately writes one `Binding` object to that Pod's `/binding`
subresource. Putting ten bindings in one Go slice does **not** turn them into one API
request.

The optimized arm in this lab is therefore a **coalesced, bounded-concurrency binder**:

```text
per-Pod Bind calls
        |
        v
short coalescing window / max batch size
        |
        v
bounded parallel execution of individual /binding writes
```

It may smooth request bursts and cap in-flight writes. It does not reduce the required
number of successful binding writes. Report it as "coalesced binding" or "bounded
binding", not as a Kubernetes batch API.

The cold-start measurement is also explicitly simulated. KWOK changes Pod status after
a configured delay; it does not pull an image, initialize CUDA, attach storage, or start
a real container.

## Division of work

| File / responsibility | Treatment | Why |
| --- | --- | --- |
| `config/kwok/pod-cold-start-stage.yaml` | vibe-code | Declarative KWOK plumbing; review selectors and units |
| `pkg/scheduler/plugins/coalescedbind/args.go` | vibe-code | API decoding, defaults, and validation |
| `pkg/scheduler/plugins/coalescedbind/plugin.go` | vibe-code except `Bind` semantics | Framework wiring and client call boilerplate |
| `cmd/scheduler/main.go` registration | vibe-code | Standard `app.WithPlugin` wiring |
| `config/scheduler/*.yaml` profiles | vibe-code | Baseline/optimized experiment plumbing |
| `scripts/exp3-*.sh` | vibe-code | Repeated setup, collection, and assertions |
| `pkg/scheduler/plugins/coalescedbind/batcher.go` | **HAND-WRITE** | Ownership, races, cancellation, and backpressure are the lesson |
| `pkg/scheduler/plugins/coalescedbind/batcher_test.go` | **HAND-WRITE first** | Defines the concurrency contract before implementation |
| Experiment interpretation | **HAND-WRITE** | Prevents claiming fewer writes or real GPU cold start |

## Goals for today

- [ ] Install a label-scoped KWOK Stage with configurable delay and jitter.
- [ ] Verify `bound -> Ready=True` reflects the configured simulated delay.
- [x] Write the batcher contract and behavioral tests.
- [x] Implement bounded coalescing without leaking or double-completing requests.
- [ ] Run baseline and optimized arms with identical load and cold-start parameters.
- [ ] Compare request count, in-flight peak, APF queueing, 429s, binding latency, and
  end-to-end latency.

## Vibe-codable code

These snippets are plumbing. They are intentionally complete enough to copy, but still
need version review against the repository's pinned Kubernetes/KWOK versions.

### 1. Label-scoped KWOK cold-start Stage

Create `config/kwok/pod-cold-start-stage.yaml`. Substitute the two placeholders from the
experiment script before applying it.

```yaml
apiVersion: kwok.x-k8s.io/v1alpha1
kind: Stage
metadata:
  # Override KWOK's default fast ready Stage instead of racing it.
  name: pod-ready
spec:
  resourceRef:
    apiGroup: v1
    kind: Pod
  selector:
    matchLabels:
      gpu-lab/simulated-cold-start: "true"
    matchExpressions:
      - key: '.metadata.deletionTimestamp'
        operator: DoesNotExist
      - key: '.status.phase'
        operator: In
        values: [Pending]
      - key: '.status.conditions.[] | select(.type == "Ready") | .status'
        operator: NotIn
        values: ["True"]
  delay:
    durationMilliseconds: COLD_START_MIN_MS
    jitterDurationMilliseconds: COLD_START_MAX_MS
  next:
    statusTemplate: |
      {{ $now := Now }}
      conditions:
        - lastTransitionTime: {{ $now | Quote }}
          status: "True"
          type: Initialized
        - lastTransitionTime: {{ $now | Quote }}
          status: "True"
          type: Ready
        - lastTransitionTime: {{ $now | Quote }}
          status: "True"
          type: ContainersReady
      containerStatuses:
      {{ range .spec.containers }}
        - image: {{ .image | Quote }}
          name: {{ .name | Quote }}
          ready: true
          restartCount: 0
          state:
            running:
              startedAt: {{ $now | Quote }}
      {{ end }}
      phase: Running
      startTime: {{ $now | Quote }}
```

This config deliberately has the same kind/name as KWOK's default `pod-ready` Stage so
the merged configuration replaces it instead of racing it. It is label-scoped, so this
experiment cluster will auto-ready only Pods submitted with `--simulated-cold-start`.
Inspect the merged configuration with
`kwokctl config view --config <rendered-file>` before creating the cluster.

### 2. Plugin arguments

```go
type CoalescedBindArgs struct {
	metav1.TypeMeta `json:",inline"`
	Window          metav1.Duration `json:"window,omitempty"`
	MaxBatch        int32           `json:"maxBatch,omitempty"`
	MaxInFlight     int32           `json:"maxInFlight,omitempty"`
	QueueCapacity   int32           `json:"queueCapacity,omitempty"`
}
```

Recommended starting values for a local experiment, not production defaults:

```yaml
window: 5ms
maxBatch: 16
maxInFlight: 8
queueCapacity: 256
```

Validation is ordinary plumbing:

```text
window >= 0
maxBatch > 0
maxInFlight > 0
queueCapacity >= maxBatch
```

### 3. Framework adapter

The adapter should stay thin. It converts framework inputs into a batcher request and
waits for exactly one result.

```go
var _ framework.BindPlugin = &CoalescedBind{}

func (p *CoalescedBind) Name() string { return Name }

func (p *CoalescedBind) Bind(
	ctx context.Context,
	state *framework.CycleState,
	pod *v1.Pod,
	nodeName string,
) *framework.Status {
	err := p.batcher.Submit(ctx, BindRequest{
		Pod:      pod.DeepCopy(),
		NodeName: nodeName,
	})
	if err != nil {
		return framework.AsStatus(err)
	}
	return nil
}
```

The worker's leaf operation remains one API call per Pod:

```go
func bindOne(ctx context.Context, client kubernetes.Interface, req BindRequest) error {
	return client.CoreV1().Pods(req.Pod.Namespace).Bind(ctx, &v1.Binding{
		ObjectMeta: metav1.ObjectMeta{
		Name:      req.Pod.Name,
		Namespace: req.Pod.Namespace,
		UID:       req.Pod.UID,
		},
		Target: v1.ObjectReference{
			Kind: "Node",
			Name: req.NodeName,
		},
	}, metav1.CreateOptions{})
}
```

`Bind` must not return success when the request was merely enqueued. The scheduling
framework treats a successful return as a completed bind.

## Must hand-write: batcher framework

Write this contract yourself in
`pkg/scheduler/plugins/coalescedbind/batcher.go`. The signatures are supplied; the state
machine is not.

```go
type BindRequest struct {
	Pod      *v1.Pod
	NodeName string
}

type bindFn func(context.Context, BindRequest) error

type queuedBind struct {
	req    BindRequest
	ctx    context.Context
	result chan error // capacity 1; exactly one terminal send
}

type Batcher struct {
	// You decide which fields own queueing, shutdown, batching, and concurrency.
}

func NewBatcher(window time.Duration, maxBatch, maxInFlight, queueCapacity int, bind bindFn) (*Batcher, error)
func (b *Batcher) Submit(ctx context.Context, req BindRequest) error
func (b *Batcher) Close() error
```

### Required state machine

```text
accepted -> queued -> executing -> succeeded
                           `-----> failed
    |          |
    |          `---------------> canceled-before-execution
    `--------------------------> rejected-queue-full

open -> closing -> closed
```

### Invariants your implementation must preserve

1. Every accepted request produces exactly one terminal result.
2. `Submit` returns only after that request has succeeded, failed, or been canceled.
3. Queue-full behavior is explicit and non-blocking; it must not silently drop work.
4. No more than `maxInFlight` leaf binding calls execute concurrently.
5. A batch flushes when `maxBatch` is reached or `window` expires, whichever comes first.
6. Canceling one request cannot cancel siblings in the same batch.
7. `Close` rejects new work, drains or explicitly fails accepted work, and is idempotent.
8. No goroutine sends on a closed result channel. Prefer never closing per-request result
   channels; one buffered terminal send is enough.
9. The batcher never retries an ambiguous binding error by itself. A blind retry can
   duplicate side effects; let the scheduler's normal retry/reconciliation path decide.
10. Metrics and callbacks run outside internal locks.

### Tests to write before the implementation

- `TestFlushesAtMaxBatch`
- `TestFlushesWhenWindowExpires`
- `TestNeverExceedsMaxInFlight`
- `TestSubmitWaitsForItsOwnBindResult`
- `TestCanceledQueuedRequestDoesNotExecute`
- `TestCancelingOneRequestDoesNotCancelBatch`
- `TestQueueFullIsReported`
- `TestBindFailureReachesCorrectSubmitter`
- `TestCloseRejectsNewRequests`
- `TestCloseDoesNotLeakAcceptedRequests`
- `TestCloseIsIdempotent`

Run both normal and race tests:

```bash
go test ./pkg/scheduler/plugins/coalescedbind -count=100
go test -race ./pkg/scheduler/plugins/coalescedbind -count=20
```

## Baseline versus optimized experiment contract

Keep everything identical except the bind implementation:

| Variable | Baseline | Optimized |
| --- | --- | --- |
| Pod count and arrival seed | same | same |
| Scheduler profile/plugins | same | same, except Bind |
| Simulated cold-start min/max | same | same |
| Client QPS/burst | same | same |
| Bind behavior | `DefaultBinder` | `CoalescedBind` |
| Repetitions | 3+ | 3+ paired runs |

Record these checks before interpreting latency:

```text
submitted Pods == successfully bound Pods == Ready Pods
binding request successes == successfully bound Pods
no duplicate Pod UID / node decisions
no censored samples hidden from percentile output
```

The primary comparison is not "number of binds"—that should remain one per successfully
bound Pod. Compare peak in-flight API requests, APF queue depth, 429s, apiserver latency,
binding P50/P95/P99, and end-to-end P50/P95/P99. If smoothing API pressure increases bind
latency, report both sides of the tradeoff.

## What I did

- Added a concrete `batcher` behind the `Batcher` interface, with one collector, a
  fixed-size worker pool, per-request results, bounded input, timer/max-size flushing,
  cancellation, and graceful shutdown.
- Added an admission gate so `Close` can stop new submissions and wait for in-progress
  enqueue attempts before the collector performs its final drain.
- Added unit tests for validation, result ownership, both flush triggers, concurrency
  limits, cancellation, backpressure, error routing, idempotent close, and concurrent
  `Submit`/`Close`.
- Ran the package tests repeatedly, ran the race detector, and ran the full repository
  test suite.

## What I learned

### Gang scheduling and binding batching solve different problems

TopoGang acts before binding. `PreFilter`, `Reserve`, and `Permit` decide whether all
members of one distributed workload may proceed together:

```text
Gang members -> Reserve -> Permit barrier -> Allow together
```

The Batcher acts after Permit. It does not decide gang membership or provide atomic gang
binding. It controls how already-approved per-Pod binding calls reach the API server:

```text
Permit Allow -> per-Pod Bind calls -> Batcher -> bounded /binding requests
```

The two modules can be composed, but they are not one algorithm. If the experiment claim
is that Gang release creates a binding burst, both comparison arms must use the same
TopoGang configuration and differ only in `DefaultBinder` versus `CoalescedBind`.

### A Batcher is not a Kubernetes batch API

Kubernetes exposes one binding subresource operation per Pod. The Batcher can coalesce
arrival timing and bound concurrency, but 500 successfully bound Pods still require 500
successful binding operations. Its potential benefit is smoothing pressure, not reducing
the write count.

The important knobs have different meanings:

| Knob | Responsibility |
| --- | --- |
| `window` | Maximum time the first item waits for its batch to form |
| `maxBatch` | Number of items that triggers an immediate flush |
| `maxInFlight` | Maximum number of `bindFn` calls executing concurrently |
| `queueCapacity` | Amount of scheduler-local burst absorbed before explicit backpressure |

`maxInFlight` is the main control on instantaneous API-server pressure. `window` may add
latency even when the API server is healthy.

### The contract and implementation are separate concepts

`batcher_contract.go` is roughly analogous to the interface-bearing part of a C++ header:

```text
BindRequest + Batcher interface + bindFn signature
```

`batcher.go` contains the concrete implementation. Go does not require header/source
separation and has no `#include`; all `.go` files in `package coalescedbind` compile as one
package. A concrete `*batcher` implicitly satisfies `Batcher` by implementing the required
method set. The compile-time assertion makes that relationship explicit to the compiler:

```go
var _ Batcher = (*batcher)(nil)
```

### The three channel types have different ownership

```text
input:  every Submit  -> the single collector
work:   collector     -> the worker pool
result: one worker    -> the matching Submit caller
```

`input` and `work` are shared for the lifetime of the Batcher. Each `queuedBind` owns a
separate buffered `result chan error`, so Pod A cannot receive Pod B's result. Capacity one
also lets a worker publish the terminal result without blocking if the caller has already
returned because its context was canceled. The per-request result channel does not need
to be closed; exactly one terminal send is the contract.

In `case err := <-item.result`, `item.result` is the `chan error`; `<-item.result` receives
one ordinary `error` value, which is assigned to the ordinary variable `err`.

### `select` chooses ready channel operations

The admission `select` watches cancellation and attempts a non-blocking input send. Its
`default` branch reports queue-full only when no channel operation can proceed immediately.
Cases are not checked in source order: when multiple cases are ready, Go chooses among
them pseudo-randomly. Therefore a `select` containing both `closing` and `input <- item`
cannot, by itself, guarantee that no input is accepted after close begins.

That observation exposed the important shutdown race:

```text
Submit observes open -> Close drains and exits -> Submit sends -> no collector remains
```

The fix is an admission gate: under a mutex, `Submit` checks `accepting` and registers a
short-lived admission; `Close` changes `accepting` to false under the same mutex, waits for
registered admission attempts, and only then signals the collector to drain. The admission
ends after enqueue succeeds or fails—not after binding completes—or shutdown would deadlock.

### Mutex and WaitGroup answer different questions

```text
Mutex:     who may enter this critical section now?
WaitGroup: how many registered operations have not finished yet?
```

`admissionMu` protects the shared `accepting` transition and ensures no `Add` can occur
after `Close` begins waiting. `admissions` lets `Close` wait for already-authorized enqueue
attempts. `workers` separately lets the collector wait until all binding workers exit.
Neither primitive replaces the other: a WaitGroup does not protect shared variables, and
a Mutex does not express completion of previously admitted asynchronous work.

### A clean shutdown depends on channel ownership

`Close` broadcasts closing exactly once with `sync.Once` and waits on `done`; it does not
compete with the collector for input. The collector owns the current batch and timer,
flushes accepted work, closes `work`, waits for workers, and finally closes `done`.
Producers never close `input`, workers never close `work`, and nobody closes per-request
result channels. A single closer per channel avoids double-close and send-on-closed-channel
panics.

### Concurrency tests and race tests are complementary

Concurrency tests actively create simultaneous operations and assert behavior: flush
timing, result routing, maximum in-flight work, cancellation isolation, and absence of
leaks during concurrent `Submit`/`Close`. Deterministic channels such as `started`,
`release`, and a common start barrier are preferable to guessing scheduler timing with
long sleeps. Timeouts are failure safety nets, not synchronization mechanisms.

The race detector observes unsafe memory access during those executions; it does not
create meaningful concurrency by itself. Repetition explores more goroutine schedules:

```bash
go test ./pkg/scheduler/plugins/coalescedbind -count=100 -timeout=90s
GOMAXPROCS=4 go test -race ./pkg/scheduler/plugins/coalescedbind -count=20 -timeout=90s
```

A passing race detector does not prove freedom from deadlocks or incorrect results, and a
passing behavioral test does not prove memory accesses are synchronized. Both are required.

## Key results / numbers

- Batcher unit/concurrency suite: passed 100 consecutive runs.
- Batcher race suite: passed 20 consecutive runs with the Go race detector.
- Full repository `go test ./...`: passed.
- No live performance result belongs here until both experiment arms pass the
  completeness assertions.

## Blockers & how I solved them

-

## Open questions

- Does bounded concurrency reduce peak APF pressure at this scale, or only move waiting
  time into the scheduler process?
- Is a 5 ms coalescing window visible in end-to-end P99 once simulated cold start is held
  constant?
- Does host CPU/IO saturation dominate before the API-server pressure signal appears?

## Commit(s) / artifacts

- `config/kwok/pod-cold-start-stage.yaml`
- `pkg/scheduler/plugins/coalescedbind/`
- paired raw results under `experiments/exp3-burst/`

## Plan for tomorrow

- Run the paired burst matrix, generate figures only from checked-in raw data, and state
  bounded conclusions without presenting KWOK cold start as real GPU initialization.
