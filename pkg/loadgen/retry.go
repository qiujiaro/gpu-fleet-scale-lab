package loadgen

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// RetryPolicy bounds retries when the apiserver responds with HTTP 429.
type RetryPolicy struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// submitWithRetry owns the 429-only retry loop.
//
// Keep non-429 failures separate: they should normally return immediately.
// The concrete client-go adapter should make 429 errors recognizable here.
func submitWithRetry(
	ctx context.Context,
	submitter Submitter,
	req SubmitRequest,
	policy RetryPolicy,
) (result SubmitResult, attempts int, rateLimited int, err error) {
	backoff := policy.InitialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return SubmitResult{}, attempts, rateLimited, err
		}

		attempts++
		result, err = submitter.Create(ctx, req)
		if err == nil {
			return result, attempts, rateLimited, nil
		}
		if !apierrors.IsTooManyRequests(err) {
			return SubmitResult{}, attempts, rateLimited, err
		}

		rateLimited++
		if attempts > policy.MaxRetries {
			return SubmitResult{}, attempts, rateLimited, err
		}

		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return SubmitResult{}, attempts, rateLimited, ctx.Err()
		}

		backoff *= 2
		if backoff > policy.MaxBackoff {
			backoff = policy.MaxBackoff
		}
	}
}
