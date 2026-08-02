# Local Testing Guide

This document defines how to design and run local tests for GPU Fleet Scale Lab. It uses
the Day 6 coalesced-binding Batcher as the main example, but the same principles apply to
the load generator, profiler, and scheduler plugins.

## What a complete local test means

A complete local test has two levels:

```text
Code correctness
  format -> unit tests -> repeated concurrency tests -> race detector
         -> static checks -> full build and repository regression

Experiment correctness
  code correctness -> scheduler startup -> KWOK smoke
                   -> paired experiments -> artifact assertions
```

Passing `go test` once proves only that the executed assertions passed once. It does not
prove that concurrent code is race-free, that a scheduler configuration starts, or that a
live experiment produces complete samples.

## How to structure a Go test

Use four parts:

```text
Arrange: prepare inputs, fakes, channels, and expected results
Act:     call the behavior being tested
Assert:  compare observable results with the contract
Cleanup: stop goroutines and release resources
```

A test function must be in a `_test.go` file and have this shape:

```go
func TestBehavior(t *testing.T) {
	// Arrange

	// Act

	// Assert
}
```

Name tests after behavior, not implementation:

```text
Good: TestSubmitWaitsForItsOwnBindResult
Good: TestCloseRejectsNewRequests
Bad:  TestBatcher1
Bad:  TestNormal
```

Each test should prove one primary behavior. Several assertions are fine when they all
support that behavior.

## Table-driven tests

Use a table when many inputs exercise the same operation and assertion structure. Typical
examples are argument validation, parsing, and boundary values.

```go
func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   int
		wantErr error
	}{
		{name: "zero", input: 0, wantErr: ErrInvalidValue},
		{name: "negative", input: -1, wantErr: ErrInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validate error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
```

Prefer `errors.Is` to comparing error strings. Wrapped errors preserve identity:

```go
fmt.Errorf("create batcher: %w", ErrInvalidValue)
```

## Test observable behavior, not private representation

Prefer:

```text
Submitting maxBatch requests causes bindFn to start.
```

Avoid making the main assertion:

```text
The private batch slice has length maxBatch.
```

Behavioral tests survive internal changes such as replacing a slice with a ring buffer.
Tests that inspect representation make safe refactoring unnecessarily difficult.

## Use fakes instead of external systems

Unit tests for the Batcher must not call a Kubernetes API server. Inject a fake `bindFn`:

```go
func(context.Context, BindRequest) error {
	return nil
}
```

A fake can:

- record which Pod was bound;
- block until the test releases it;
- return a distinct error for each request;
- count concurrent executions;
- inspect cancellation.

This separates two questions:

```text
Unit test:        Does the Batcher implement its concurrency contract?
Integration test: Does the BindPlugin call the Kubernetes binding API correctly?
```

## Deterministic concurrency tests

Concurrency tests validate behavior under overlapping operations. They are different from
race detection: the test must first create meaningful concurrency.

### Coordinate with channels

Prefer explicit signals:

```text
started: the fake operation has begun
release: the test permits it to finish
result:  the caller has returned
```

Example flow:

```text
Submit goroutine -> bindFn -> signal started -> wait on release
Test             -> observe started -> assert Submit still blocked -> close release
Submit goroutine -> receive bind result -> return
```

This is more stable than assuming `time.Sleep` gives another goroutine enough time to run.

### Start many goroutines together

Use a start barrier:

```go
start := make(chan struct{})

go func() {
	<-start
	// concurrent operation A
}()

go func() {
	<-start
	// concurrent operation B
}()

close(start)
```

Closing `start` wakes every receiver. It creates a much larger scheduling overlap than
starting goroutines with sleeps between them.

### Add timeout safety

Never let a concurrency test wait forever:

```go
select {
case err := <-result:
	// assert err
case <-time.After(time.Second):
	t.Fatal("timed out waiting for result")
}
```

A timeout is a failure safety net, not the mechanism used to order the test.

### Make the test itself race-free

If fake workers update a shared counter, use `sync/atomic` or a Mutex. The race detector
checks test code as well as production code.

```text
Incorrect test counter: running++
Race-safe counter:      running.Add(1)
```

## Mutex and WaitGroup in tests and production

They solve different problems:

```text
Mutex:     who may access this shared critical section now?
WaitGroup: how many registered operations have not finished yet?
```

Use a Mutex to protect shared state. Use a WaitGroup to wait for a known collection of
goroutines or operations. A WaitGroup does not make reads and writes mutually exclusive;
a Mutex does not express that asynchronous work has completed.

When combining `Add` and `Wait`, establish an admission rule so no new `Add` can occur
after the final `Wait` begins. In the Batcher, `admissionMu` protects the transition from
accepting to closing, while `admissions` waits for enqueue attempts already authorized by
that transition.

## Concurrency testing versus race detection

### Concurrency test

Checks semantic properties such as:

- no more than `maxInFlight` calls execute at once;
- every accepted request receives exactly one result;
- Pod A never receives Pod B's error;
- canceling one request does not cancel its siblings;
- concurrent `Submit` and `Close` do not leak;
- graceful `Close` waits for accepted work.

### Race detector

Checks whether executed goroutines access shared memory without synchronization:

```bash
go test -race ./pkg/scheduler/plugins/coalescedbind
```

The race detector does not automatically produce useful concurrency. A sequential test
can pass `-race` while never exercising the risky path. Conversely, race-free code can
still deadlock or return the wrong result. Behavioral concurrency tests and `-race` are
both required.

## Batcher test matrix

The Day 6 Batcher should cover the following contracts:

| Test | Contract proved |
| --- | --- |
| `TestNewBatcherValidation` | Invalid configuration fails before goroutines start |
| `TestSubmitWaitsForItsOwnBindResult` | Enqueueing is not reported as binding success |
| `TestFlushesAtMaxBatch` | Reaching `maxBatch` flushes without waiting for the timer |
| `TestFlushesWhenWindowExpires` | A partial batch eventually flushes |
| `TestNeverExceedsMaxInFlight` | Fixed workers enforce the concurrency bound |
| `TestBindFailureReachesCorrectSubmitter` | Per-request result channels prevent result crossover |
| `TestCanceledQueuedRequestDoesNotExecute` | A canceled queued request skips `bindFn` |
| `TestQueueFullIsReported` | Backpressure is explicit and non-blocking |
| `TestCloseDrainsAndIsIdempotent` | Accepted work completes and repeated Close is safe |
| `TestConcurrentSubmitAndCloseDoesNotLeak` | Admission and shutdown do not strand requests |

Tests live in:

```text
pkg/scheduler/plugins/coalescedbind/batcher_test.go
```

## Local code-validation sequence

### 1. Format and whitespace

Check formatting:

```bash
gofmt -l pkg/scheduler/plugins/coalescedbind cmd/scheduler cmd/loadgen
git diff --check
```

Format files when needed:

```bash
gofmt -w pkg/scheduler/plugins/coalescedbind/*.go
```

### 2. Run one focused test

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache \
go test ./pkg/scheduler/plugins/coalescedbind \
  -run TestSubmitWaitsForItsOwnBindResult \
  -count=1 \
  -v
```

### 3. Run the package once

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache \
go test ./pkg/scheduler/plugins/coalescedbind \
  -count=1 \
  -timeout=30s
```

### 4. Repeat concurrent tests

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache \
go test ./pkg/scheduler/plugins/coalescedbind \
  -count=100 \
  -timeout=90s
```

Run the shutdown race more aggressively:

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache \
go test ./pkg/scheduler/plugins/coalescedbind \
  -run TestConcurrentSubmitAndCloseDoesNotLeak \
  -count=500 \
  -timeout=90s
```

### 5. Run the race detector

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache \
GOMAXPROCS=4 \
go test -race ./pkg/scheduler/plugins/coalescedbind \
  -count=20 \
  -timeout=90s
```

Also test single-thread scheduling when investigating ordering assumptions:

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache \
GOMAXPROCS=1 \
go test ./pkg/scheduler/plugins/coalescedbind \
  -count=100 \
  -timeout=90s
```

### 6. Static checks

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache go vet ./...
```

### 7. Full repository regression and build

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache go test ./...
GOCACHE=/tmp/gpu-fleet-test-go-cache go build ./...
```

### 8. Shell syntax and generated configuration

```bash
bash -n \
  scripts/render-day6-cold-start.sh \
  scripts/exp3-binding-arm.sh

COLD_START_MIN_MS=1000 \
COLD_START_MAX_MS=3000 \
OUTPUT=/tmp/day6-cold-start.yaml \
./scripts/render-day6-cold-start.sh

rg 'COLD_START_(MIN|MAX)_MS' /tmp/day6-cold-start.yaml
rg -n 'durationMilliseconds|jitterDurationMilliseconds' /tmp/day6-cold-start.yaml
```

The first `rg` should produce no output; the second should show the rendered values.

## Scheduler startup smoke

Build the binary:

```bash
GOCACHE=/tmp/gpu-fleet-test-go-cache \
go build -o /tmp/gpu-lab-scheduler ./cmd/scheduler
```

Start one scheduler profile at a time:

```bash
/tmp/gpu-lab-scheduler \
  --config config/scheduler/day6-baseline-config.yaml \
  --kubeconfig "$KUBECONFIG" \
  --secure-port 10260
```

or:

```bash
/tmp/gpu-lab-scheduler \
  --config config/scheduler/day6-optimized-config.yaml \
  --kubeconfig "$KUBECONFIG" \
  --secure-port 10260
```

Check health from another terminal:

```bash
curl -ksSf https://127.0.0.1:10260/healthz
```

Startup success means more than a running process. Confirm that the selected profile and
plugin loaded, the port is not shared with another scheduler, and logs contain no unknown
plugin or configuration-decoding errors.

## KWOK and experiment smoke

Run a small experiment before a full matrix:

```bash
ARM=baseline \
TARGET_QPS=5 \
DURATION_SECONDS=5 \
CLEANUP_PODS=false \
./scripts/exp3-binding-arm.sh
```

Repeat with the optimized scheduler running:

```bash
ARM=optimized \
TARGET_QPS=5 \
DURATION_SECONDS=5 \
CLEANUP_PODS=false \
./scripts/exp3-binding-arm.sh
```

Each run must satisfy:

```text
submitted == successfully created
submitted == observed
submitted == bound
submitted == Ready
censored samples == 0
duplicate Pod UIDs == 0
failed binding requests == 0
```

An incomplete run is a diagnostic artifact, not a valid latency sample.

## Paired experiment discipline

Keep these variables identical between baseline and optimized arms:

- node count and occupancy;
- Pod count and workload shape;
- arrival model, QPS, burst, and seed;
- scheduler client QPS and burst;
- simulated cold-start distribution;
- profiler duration and drain period.

Change only the binding implementation. Use paired, alternating runs:

```text
baseline(seed=42) -> optimized(seed=42)
baseline(seed=43) -> optimized(seed=43)
baseline(seed=44) -> optimized(seed=44)
```

Do not claim improvement from a lower APF queue alone. Check whether waiting simply moved
from the API server into the scheduler:

| Layer | Evidence |
| --- | --- |
| Batcher | queue length, queue wait, in-flight binds, queue-full count |
| API server/APF | in-queue peak, queue wait, request P99, 429 count |
| End-to-end | binding and submit-to-Ready P50/P95/P99, censored count |

The number of successful binding requests should remain one per successfully bound Pod.
Coalescing controls timing and concurrency; it does not create a multi-Pod Kubernetes API.

## Completion checklist

Code correctness:

- [ ] `gofmt -l` has no output.
- [ ] `git diff --check` passes.
- [ ] Focused unit tests pass.
- [ ] Package tests pass 100 repeated runs.
- [ ] Race tests pass 20 repeated runs.
- [ ] `go vet ./...` passes.
- [ ] `go test ./...` passes.
- [ ] `go build ./...` passes.
- [ ] Shell syntax checks pass.

Experiment correctness:

- [ ] Baseline scheduler starts and serves `/healthz`.
- [ ] Optimized scheduler starts and serves `/healthz`.
- [ ] The KWOK simulated cold-start delay is observed and clearly labeled simulated.
- [ ] Baseline and optimized smoke runs produce complete samples.
- [ ] Paired runs use identical controlled variables.
- [ ] Scheduler, APF, and end-to-end metrics are collected.
- [ ] Conclusions report latency tradeoffs and do not claim fewer binding writes.

