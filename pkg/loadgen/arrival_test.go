package loadgen

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestConstant_Interval(t *testing.T) {
	c := Constant{RatePerSec: 10}
	if got := c.Next(0); got != 100*time.Millisecond {
		t.Fatalf("want 100ms, got %v", got)
	}
}

// Poisson sample mean should approach 1/λ (with a fixed seed and a loose tolerance).
func TestPoisson_MeanApproxInverseLambda(t *testing.T) {
	p := Poisson{Lambda: 5, R: rand.New(rand.NewSource(1))}
	const n = 20000
	var sum float64
	for i := 0; i < n; i++ {
		sum += p.Next(0).Seconds()
	}
	mean := sum / n
	want := 1.0 / 5.0
	if math.Abs(mean-want) > 0.02 {
		t.Fatalf("poisson mean %.4f too far from %.4f", mean, want)
	}
}

func TestBurst_FiresOnceAtThreshold(t *testing.T) {
	b := &Burst{SteadyRatePerSec: 10, At: time.Second, SpikeCount: 100}
	if d := b.Next(500 * time.Millisecond); d != 100*time.Millisecond {
		t.Fatalf("pre-spike want steady 100ms, got %v", d)
	}
	if d := b.Next(time.Second); d != 0 {
		t.Fatalf("at threshold want 0 (spike), got %v", d)
	}
	if d := b.Next(2 * time.Second); d == 0 {
		t.Fatalf("spike should fire only once")
	}
}

func TestBurst_ZeroPreloadWaitsUntilThreshold(t *testing.T) {
	b := &Burst{At: 30 * time.Second, SpikeCount: 500}
	if d := b.Next(0); d != 30*time.Second {
		t.Fatalf("zero-preload burst should wait 30s, got %v", d)
	}
	if d := b.Next(30 * time.Second); d != 0 {
		t.Fatalf("burst at threshold should return zero, got %v", d)
	}
}

func TestFormalExp2SeedsStayWithinFivePercentOfTargetRate(t *testing.T) {
	for _, seed := range []int64{42, 43, 44} {
		arrival := Poisson{Lambda: 40, R: rand.New(rand.NewSource(seed))}
		var firstToLast time.Duration
		for i := 0; i < 800; i++ {
			delay := arrival.Next(0)
			if i > 0 {
				firstToLast += delay
			}
		}
		actual := 799 / firstToLast.Seconds()
		ratio := actual / 40
		if ratio < 0.95 || ratio > 1.05 {
			t.Fatalf("seed %d produces %.3f Pod/s (ratio %.4f), outside ±5%%", seed, actual, ratio)
		}
	}
}
