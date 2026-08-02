package coalescedbind

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrInvalidWindow        = errors.New("invalid window")
	ErrInvalidMaxBatch      = errors.New("invalid maxBatch")
	ErrInvalidMaxInFlight   = errors.New("invalid maxInFlight")
	ErrInvalidQueueCapacity = errors.New("invalid queueCapacity")
	ErrBatcherClosed        = errors.New("batcher closed")
	ErrBatcherQueueFull     = errors.New("batcher queue full")
)

// queuedBind is one accepted Submit call. Each request owns its result channel so a
// caller can receive only its own binding result.
type queuedBind struct {
	ctx    context.Context
	req    BindRequest
	result chan error
}

// batcher is the concrete implementation of the Batcher contract.
//
// Ownership rules to preserve while implementing it:
//   - Submit is a producer of input tasks.
//   - exactly one collector owns the current batch and its timer.
//   - workers consume flushed tasks and call bind.
//   - the collector is the only goroutine allowed to close work.
type batcher struct {
	window      time.Duration
	maxBatch    int
	maxInFlight int
	bind        bindFn

	input chan *queuedBind
	work  chan *queuedBind

	closing chan struct{}
	done    chan struct{}

	admissionMu sync.Mutex
	accepting   bool
	admissions  sync.WaitGroup
	closeOnce   sync.Once
	workers     sync.WaitGroup
}

var _ Batcher = (*batcher)(nil)

// newBatcher validates the configuration, initializes the channels, starts one
// collector and maxInFlight workers, and returns the concrete batcher.
func newBatcher(
	window time.Duration,
	maxBatch int,
	maxInFlight int,
	queueCapacity int,
	bind bindFn,
) (Batcher, error) {
	if window <= 0 {
		return nil, ErrInvalidWindow
	}
	if maxBatch <= 0 {
		return nil, ErrInvalidMaxBatch
	}
	if maxInFlight <= 0 {
		return nil, ErrInvalidMaxInFlight
	}
	if queueCapacity <= 0 || queueCapacity < maxBatch {
		return nil, ErrInvalidQueueCapacity
	}
	if bind == nil {
		return nil, fmt.Errorf("bind function is required")
	}

	b := &batcher{
		window:      window,
		maxBatch:    maxBatch,
		maxInFlight: maxInFlight,
		bind:        bind,
		input:       make(chan *queuedBind, queueCapacity),
		work:        make(chan *queuedBind, queueCapacity),
		closing:     make(chan struct{}),
		done:        make(chan struct{}),
		accepting:   true,
	}

	b.workers.Add(maxInFlight)
	for range maxInFlight {
		go b.runWorker()
	}
	go b.runCollector()

	return b, nil
}

// Submit accepts one binding request and waits for that request's terminal result.
func (b *batcher) Submit(ctx context.Context, req BindRequest) error {
	if ctx == nil {
		return fmt.Errorf("submit bind request: context is nil")
	}
	if req.Pod == nil {
		return fmt.Errorf("submit bind request: pod is nil")
	}
	if req.NodeName == "" {
		return fmt.Errorf("submit bind request: node name is empty")
	}

	item := &queuedBind{
		ctx:    ctx,
		req:    req,
		result: make(chan error, 1),
	}

	// Close changes accepting under the same lock before it waits for admissions.
	// Consequently, once Close starts waiting, no later Submit can register here.
	b.admissionMu.Lock()
	if !b.accepting {
		b.admissionMu.Unlock()
		return ErrBatcherClosed
	}
	b.admissions.Add(1)
	b.admissionMu.Unlock()

	select {
	case <-ctx.Done():
		b.admissions.Done()
		return ctx.Err()
	case b.input <- item:
		b.admissions.Done()
	default:
		b.admissions.Done()
		return ErrBatcherQueueFull
	}

	select {
	case err := <-item.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close rejects new submissions, drains work that was already accepted, and waits for
// the collector and all workers to exit. It must be safe to call more than once.
func (b *batcher) Close() error {
	b.closeOnce.Do(func() {
		b.admissionMu.Lock()
		b.accepting = false
		b.admissionMu.Unlock()

		// Wait only for Submit's admission section, never for its result wait. This
		// guarantees the collector sees every accepted input before its final drain.
		b.admissions.Wait()
		close(b.closing)
	})

	<-b.done
	return nil
}

// runCollector owns the current batch. It flushes on maxBatch, window expiry, or
// shutdown, and is the only goroutine that closes b.work.
func (b *batcher) runCollector() {
	batch := make([]*queuedBind, 0, b.maxBatch)
	timer := time.NewTimer(b.window)
	if !timer.Stop() {
		<-timer.C
	}
	var timerC <-chan time.Time

	stopTimer := func() {
		if timerC == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
	}
	flush := func() {
		stopTimer()
		for _, item := range batch {
			b.work <- item
		}
		batch = batch[:0]
	}
	add := func(item *queuedBind) {
		batch = append(batch, item)
		if len(batch) == 1 {
			timer.Reset(b.window)
			timerC = timer.C
		}
		if len(batch) == b.maxBatch {
			flush()
		}
	}

	defer func() {
		stopTimer()
		close(b.work)
		b.workers.Wait()
		close(b.done)
	}()

	for {
		select {
		case item := <-b.input:
			add(item)
		case <-timerC:
			timerC = nil
			for _, item := range batch {
				b.work <- item
			}
			batch = batch[:0]
		case <-b.closing:
			for {
				select {
				case item := <-b.input:
					add(item)
				default:
					flush()
					return
				}
			}
		}
	}
}

// runWorker executes flushed requests. A fixed number of these goroutines provides the
// maxInFlight limit without an additional semaphore.
func (b *batcher) runWorker() {
	defer b.workers.Done()

	for item := range b.work {
		if err := item.ctx.Err(); err != nil {
			item.result <- err
			continue
		}

		item.result <- b.bind(item.ctx, item.req)
	}
}
