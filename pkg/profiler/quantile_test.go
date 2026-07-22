package profiler

import (
	"testing"
	"time"
)

func d(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }

func TestQuantiles_NearestRank(t *testing.T) {
	// 1..100 ms
	var s []time.Duration
	for i := 1; i <= 100; i++ {
		s = append(s, d(i))
	}
	q := Quantiles(s, 0.5, 0.95, 0.99)
	if q[0.5] != d(50) {
		t.Errorf("P50 want 50ms, got %v", q[0.5])
	}
	if q[0.95] != d(95) {
		t.Errorf("P95 want 95ms, got %v", q[0.95])
	}
	if q[0.99] != d(99) {
		t.Errorf("P99 want 99ms, got %v", q[0.99])
	}
}

func TestQuantiles_Empty(t *testing.T) {
	if got := Quantiles(nil, 0.99); len(got) != 0 {
		t.Fatalf("empty input should give empty map, got %v", got)
	}
}

func TestCensoredRate(t *testing.T) {
	ts := []PodTimeline{{Censored: false}, {Censored: true}, {Censored: true}, {Censored: false}}
	if got := CensoredRate(ts); got != 0.5 {
		t.Fatalf("want 0.5, got %v", got)
	}
}
