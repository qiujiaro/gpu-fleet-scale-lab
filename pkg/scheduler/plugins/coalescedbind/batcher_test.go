package coalescedbind

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testBindRequest(name string) BindRequest {
	return BindRequest{
		Pod:      &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}},
		NodeName: "node-1",
	}
}

func submitAsync(b Batcher, ctx context.Context, req BindRequest) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- b.Submit(ctx, req)
	}()
	return result
}

func receiveError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Submit")
		return nil
	}
}

func TestNewBatcherValidation(t *testing.T) {
	validBind := func(context.Context, BindRequest) error { return nil }
	tests := []struct {
		name          string
		window        time.Duration
		maxBatch      int
		maxInFlight   int
		queueCapacity int
		bind          bindFn
		want          error
	}{
		{"zero window", 0, 1, 1, 1, validBind, ErrInvalidWindow},
		{"negative window", -time.Millisecond, 1, 1, 1, validBind, ErrInvalidWindow},
		{"zero max batch", time.Millisecond, 0, 1, 1, validBind, ErrInvalidMaxBatch},
		{"zero max in flight", time.Millisecond, 1, 0, 1, validBind, ErrInvalidMaxInFlight},
		{"queue smaller than batch", time.Millisecond, 2, 1, 1, validBind, ErrInvalidQueueCapacity},
		{"nil bind", time.Millisecond, 1, 1, 1, nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batcher, err := newBatcher(tt.window, tt.maxBatch, tt.maxInFlight, tt.queueCapacity, tt.bind)
			if err == nil {
				if batcher != nil {
					_ = batcher.Close()
				}
				t.Fatal("newBatcher returned nil error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("newBatcher error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSubmitWaitsForItsOwnBindResult(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	wantErr := errors.New("bind failed")
	b, err := newBatcher(time.Millisecond, 1, 1, 1, func(context.Context, BindRequest) error {
		close(started)
		<-release
		return wantErr
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	result := submitAsync(b, context.Background(), testBindRequest("pod-1"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("bind did not start")
	}
	select {
	case err := <-result:
		t.Fatalf("Submit returned before bind completed: %v", err)
	default:
	}

	close(release)
	if err := receiveError(t, result); !errors.Is(err, wantErr) {
		t.Fatalf("Submit error = %v, want %v", err, wantErr)
	}
}

func TestFlushesAtMaxBatch(t *testing.T) {
	started := make(chan string, 3)
	b, err := newBatcher(time.Hour, 3, 3, 3, func(_ context.Context, req BindRequest) error {
		started <- req.Pod.Name
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	results := make([]<-chan error, 0, 3)
	for i := 0; i < 3; i++ {
		results = append(results, submitAsync(b, context.Background(), testBindRequest(fmt.Sprintf("pod-%d", i))))
	}
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("batch did not flush after reaching maxBatch")
		}
	}
	for _, result := range results {
		if err := receiveError(t, result); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFlushesWhenWindowExpires(t *testing.T) {
	started := make(chan struct{}, 1)
	b, err := newBatcher(20*time.Millisecond, 4, 1, 4, func(context.Context, BindRequest) error {
		started <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	result := submitAsync(b, context.Background(), testBindRequest("pod-1"))
	select {
	case <-started:
		t.Fatal("partial batch flushed before its window expired")
	case <-time.After(5 * time.Millisecond):
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("partial batch did not flush after its window expired")
	}
	if err := receiveError(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestNeverExceedsMaxInFlight(t *testing.T) {
	const total = 8
	var running atomic.Int32
	var observedMax atomic.Int32
	started := make(chan struct{}, total)
	release := make(chan struct{})
	b, err := newBatcher(time.Hour, total, 2, total, func(context.Context, BindRequest) error {
		current := running.Add(1)
		for {
			old := observedMax.Load()
			if current <= old || observedMax.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		running.Add(-1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	results := make([]<-chan error, 0, total)
	for i := 0; i < total; i++ {
		results = append(results, submitAsync(b, context.Background(), testBindRequest(fmt.Sprintf("pod-%d", i))))
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("expected two concurrent workers")
		}
	}
	select {
	case <-started:
		t.Fatal("more than maxInFlight binds started")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	for _, result := range results {
		if err := receiveError(t, result); err != nil {
			t.Fatal(err)
		}
	}
	if got := observedMax.Load(); got != 2 {
		t.Fatalf("maximum in-flight binds = %d, want 2", got)
	}
}

func TestBindFailureReachesCorrectSubmitter(t *testing.T) {
	wantErr := errors.New("pod-a failed")
	b, err := newBatcher(time.Hour, 2, 2, 2, func(_ context.Context, req BindRequest) error {
		if req.Pod.Name == "pod-a" {
			return wantErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	a := submitAsync(b, context.Background(), testBindRequest("pod-a"))
	c := submitAsync(b, context.Background(), testBindRequest("pod-b"))
	if err := receiveError(t, a); !errors.Is(err, wantErr) {
		t.Fatalf("pod-a error = %v, want %v", err, wantErr)
	}
	if err := receiveError(t, c); err != nil {
		t.Fatalf("pod-b error = %v, want nil", err)
	}
}

func TestCanceledQueuedRequestDoesNotExecute(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var called sync.Map
	b, err := newBatcher(time.Hour, 1, 1, 2, func(_ context.Context, req BindRequest) error {
		called.Store(req.Pod.Name, true)
		if req.Pod.Name == "pod-1" {
			close(firstStarted)
			<-releaseFirst
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	first := submitAsync(b, context.Background(), testBindRequest("pod-1"))
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first bind did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	second := submitAsync(b, ctx, testBindRequest("pod-2"))
	cancel()
	if err := receiveError(t, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Submit error = %v, want context.Canceled", err)
	}
	close(releaseFirst)
	if err := receiveError(t, first); err != nil {
		t.Fatal(err)
	}

	// Wait until the worker has consumed the canceled work item.
	time.Sleep(10 * time.Millisecond)
	if _, ok := called.Load("pod-2"); ok {
		t.Fatal("bind function executed for a canceled queued request")
	}
}

func TestQueueFullIsReported(t *testing.T) {
	b := &batcher{
		input:     make(chan *queuedBind, 1),
		closing:   make(chan struct{}),
		accepting: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := submitAsync(b, ctx, testBindRequest("pod-1"))
	deadline := time.Now().Add(time.Second)
	for len(b.input) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(b.input) != 1 {
		t.Fatal("first request did not fill input queue")
	}
	if err := b.Submit(context.Background(), testBindRequest("pod-2")); !errors.Is(err, ErrBatcherQueueFull) {
		t.Fatalf("second Submit error = %v, want %v", err, ErrBatcherQueueFull)
	}
	cancel()
	if err := receiveError(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Submit error = %v, want context.Canceled", err)
	}
}

func TestCloseDrainsAndIsIdempotent(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	b, err := newBatcher(time.Hour, 1, 1, 10, func(context.Context, BindRequest) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result := submitAsync(b, context.Background(), testBindRequest("pod-1"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("accepted bind did not start")
	}

	closed := make(chan error, 2)
	go func() { closed <- b.Close() }()
	go func() { closed <- b.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before accepted work finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := receiveError(t, result); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := receiveError(t, closed); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Submit(context.Background(), testBindRequest("pod-2")); !errors.Is(err, ErrBatcherClosed) {
		t.Fatalf("Submit after Close error = %v, want %v", err, ErrBatcherClosed)
	}
}

func TestConcurrentSubmitAndCloseDoesNotLeak(t *testing.T) {
	const submissions = 200
	b, err := newBatcher(time.Millisecond, 8, 4, 32, func(context.Context, BindRequest) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, submissions)
	for i := 0; i < submissions; i++ {
		go func(index int) {
			<-start
			results <- b.Submit(context.Background(), testBindRequest(fmt.Sprintf("pod-%d", index)))
		}(i)
	}
	closed := make(chan error, 1)
	go func() {
		<-start
		closed <- b.Close()
	}()
	close(start)

	for i := 0; i < submissions; i++ {
		select {
		case err := <-results:
			if err != nil && !errors.Is(err, ErrBatcherClosed) && !errors.Is(err, ErrBatcherQueueFull) {
				t.Fatalf("Submit returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Submit leaked during Close")
		}
	}
	if err := receiveError(t, closed); err != nil {
		t.Fatal(err)
	}
}
