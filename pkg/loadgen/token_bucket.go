package loadgen

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

type TokenBucket struct {
	mu sync.Mutex

	tokens   float64
	capacity float64
	rate     float64
	last     time.Time
}

func NewTokenBucket(qps float64, burst int) *TokenBucket {
	return &TokenBucket{
		tokens:   float64(burst),
		capacity: float64(burst),
		rate:     qps,
		last:     time.Now(),
	}
}

func (b *TokenBucket) Wait(ctx context.Context) error {
	if b.rate <= 0 || b.capacity <= 0 {
		return errors.New("token bucket rate and capacity must be positive")
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		b.mu.Lock()

		now := time.Now()
		b.refill(now)

		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}

		missing := 1 - b.tokens
		waitSeconds := missing / b.rate
		waitDuration := time.Duration(math.Ceil(
			waitSeconds * float64(time.Second),
		))

		b.mu.Unlock()

		timer := time.NewTimer(waitDuration)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		}
	}
}

func (b *TokenBucket) refill(now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}

	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}

	b.last = now
}
