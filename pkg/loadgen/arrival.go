// Package loadgen implements arrival models and the controlled-rate submission driver.
//
// This is where the key algorithms of hand-written core module #1 live. The ArrivalModel
// implementations and the token-bucket wiring must be written and understood by you.
package loadgen

import (
	"math"
	"math/rand"
	"time"
)

// ArrivalModel returns the delay until the next submission event.
// Three implementations: constant / poisson / burst. Interviewers may ask how each
// affects tail latency (P99).
type ArrivalModel interface {
	// Next returns the wait before the next arrival. t is the elapsed time since start.
	Next(t time.Duration) time.Duration
}

// Constant: fixed interval = 1/rate.
type Constant struct{ RatePerSec float64 }

func (c Constant) Next(_ time.Duration) time.Duration {
	if c.RatePerSec <= 0 {
		return time.Hour
	}
	return time.Duration(float64(time.Second) / c.RatePerSec)
}

// Poisson: exponentially distributed intervals, interval = -ln(U)/λ. Uses an injected
// *rand.Rand for reproducibility.
type Poisson struct {
	Lambda float64 // arrivals per second
	R      *rand.Rand
}

func (p Poisson) Next(_ time.Duration) time.Duration {
	if p.Lambda <= 0 {
		return time.Hour
	}
	u := p.R.Float64()
	if u <= 0 {
		u = math.SmallestNonzeroFloat64
	}
	sec := -math.Log(u) / p.Lambda
	return time.Duration(sec * float64(time.Second))
}

// Burst: steady SteadyRatePerSec while t < At; once t reaches At, submit SpikeCount pods
// at once (via a 0 interval). The "N at once" behavior is realized by the driver together
// with a remainingSpike counter; here we just define the interval semantics.
type Burst struct {
	SteadyRatePerSec float64
	At               time.Duration
	SpikeCount       int
	spikeFired       bool
}

func (b *Burst) Next(t time.Duration) time.Duration {
	if !b.spikeFired && t >= b.At {
		b.spikeFired = true
		return 0 // driver sees 0 and fires SpikeCount pods back-to-back
	}
	if !b.spikeFired && b.SteadyRatePerSec <= 0 {
		return b.At - t
	}
	if b.SteadyRatePerSec <= 0 {
		return time.Hour
	}
	return time.Duration(float64(time.Second) / b.SteadyRatePerSec)
}
