// Package profiler decomposes pod lifecycle latency and computes P50/P95/P99.
//
// This is hand-written core module #2. The quantile math and censored-sample handling
// are interview deep-dive topics and must be written by you.
package profiler

import (
	"sort"
	"time"
)

// PodTimeline records the four key moments of a pod's lifecycle.
// Censored=true means the pod had not reached a terminal state when the run ended
// (do not treat it as 0, do not drop it — count it separately).
type PodTimeline struct {
	UID         string
	Name        string
	GroupID     string
	MinMember   int
	MemberIndex int
	Attempts    int
	SubmitTs    time.Time
	ScheduledTs time.Time // PodScheduled condition / spec.nodeName written
	BoundTs     time.Time
	ReadyTs     time.Time // client-side first observation of Ready=True
	Censored    bool
}

func (p PodTimeline) Scheduling() time.Duration { return p.ScheduledTs.Sub(p.SubmitTs) }
func (p PodTimeline) Binding() time.Duration    { return p.BoundTs.Sub(p.ScheduledTs) }
func (p PodTimeline) ColdStart() time.Duration  { return p.ReadyTs.Sub(p.BoundTs) }
func (p PodTimeline) EndToEnd() time.Duration   { return p.ReadyTs.Sub(p.SubmitTs) }

// Quantiles computes quantiles using the nearest-rank method (explicit definition, easy
// to explain in interviews). qs are e.g. 0.5, 0.95, 0.99. Empty input returns an empty map.
func Quantiles(durs []time.Duration, qs ...float64) map[float64]time.Duration {
	out := make(map[float64]time.Duration, len(qs))
	if len(durs) == 0 {
		return out
	}
	s := append([]time.Duration(nil), durs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	for _, q := range qs {
		if q <= 0 {
			out[q] = s[0]
			continue
		}
		if q >= 1 {
			out[q] = s[n-1]
			continue
		}
		// nearest-rank: rank = ceil(q*n), index = rank-1
		rank := int(float64(n)*q + 0.9999999)
		if rank < 1 {
			rank = 1
		}
		if rank > n {
			rank = n
		}
		out[q] = s[rank-1]
	}
	return out
}

// CensoredRate returns the fraction of censored samples, for honest reporting.
func CensoredRate(ts []PodTimeline) float64 {
	if len(ts) == 0 {
		return 0
	}
	var c int
	for _, t := range ts {
		if t.Censored {
			c++
		}
	}
	return float64(c) / float64(len(ts))
}
