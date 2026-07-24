package loadgen

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// SubmitRequest is the information needed to construct and submit one Pod.
// The Kubernetes/client-go adapter can implement Submitter in a separate file.
type SubmitRequest struct {
	Name          string
	Namespace     string
	GPU           int
	SchedulerName string
	PodGroupUID   string
}

// SubmitResult contains fields returned by the apiserver after Pod creation.
type SubmitResult struct {
	Name string
	UID  string
}

// Submitter isolates client-go from the producer/worker/limiter logic and makes
// Run testable with a fake implementation.
type Submitter interface {
	Create(ctx context.Context, req SubmitRequest) (SubmitResult, error)
}

// Stats summarizes one generator run.
type Stats struct {
	Attempted   int64
	Succeeded   int64
	Failed      int64
	RateLimited int64
}

// Run coordinates arrivals, rate limiting, workers, retries, and recording.
//
// Hand-write here:
//  1. validate spec and create a duration-scoped context
//  2. create the token bucket from spec.MaxQPS and spec.Burst
//  3. producer: turn Arrival.Next(...) calls into work items
//  4. workers: wait for a token, submit, retry 429, then record success
//  5. close channels in the correct ownership order and aggregate Stats

func Run(
	ctx context.Context,
	submitter Submitter,
	spec WorkloadSpec,
	rec *Recorder,
) (Stats, error) {
	if err := spec.Validate(); err != nil {
		return Stats{}, fmt.Errorf("invalid workload spec: %w", err)
	}
	if submitter == nil {
		return Stats{}, fmt.Errorf("submitter must not be nil")
	}
	if rec == nil {
		return Stats{}, fmt.Errorf("recorder must not be nil")
	}

	runCtx, cancel := context.WithTimeout(ctx, spec.Duration)
	defer cancel()

	tokenBucket := NewTokenBucket(spec.MaxQPS, spec.Burst)
	workCh := make(chan SubmitRequest, spec.Workers*2)

	go func() {
		defer close(workCh)

		start := time.Now()
		sequence := 0
		for {
			elapsed := time.Since(start)
			delay := spec.Arrival.Next(elapsed)
			timer := time.NewTimer(delay)
			select {
			case <-runCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			count := 1
			if delay == 0 {
				if burst, ok := spec.Arrival.(*Burst); ok {
					count = burst.SpikeCount
				}
			}

			for i := 0; i < count; i++ {
				req := SubmitRequest{
					Name:          fmt.Sprintf("loadgen-%d-", sequence),
					Namespace:     spec.Namespace,
					GPU:           spec.GPU,
					SchedulerName: spec.SchedulerName,
					PodGroupUID:   spec.PodGroupUID,
				}
				sequence++
				select {
				case workCh <- req:
				case <-runCtx.Done():
					return
				}
			}
		}
	}()

	retryPolicy := RetryPolicy{
		MaxRetries:     5,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     2 * time.Second,
	}

	var stats Stats
	var workers sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for i := 0; i < spec.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for req := range workCh {
				if err := tokenBucket.Wait(runCtx); err != nil {
					return
				}

				result, attempts, rateLimited, err := submitWithRetry(
					runCtx, submitter, req, retryPolicy,
				)
				atomic.AddInt64(&stats.Attempted, int64(attempts))
				atomic.AddInt64(&stats.RateLimited, int64(rateLimited))
				if err != nil {
					atomic.AddInt64(&stats.Failed, 1)
					continue
				}

				atomic.AddInt64(&stats.Succeeded, 1)
				if err := rec.Record(Record{
					Name:     result.Name,
					UID:      result.UID,
					SubmitTS: time.Now(),
					Attempts: attempts,
				}); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("record result: %w", err)
						cancel()
					}
					errMu.Unlock()
					return
				}
			}
		}()
	}

	workers.Wait()

	errMu.Lock()
	err := firstErr
	errMu.Unlock()
	return stats, err
}
