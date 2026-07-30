package profiler

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"time"
)

// GroupTimeline is the gang-level projection used by Exp2-P. A group is complete only
// when exactly its declared minMember (or more) were submitted and every member reached
// Ready before the cutoff.
type GroupTimeline struct {
	GroupID             string
	MinMember           int
	MemberCount         int
	FirstSubmitTs       time.Time
	LastSubmitTs        time.Time
	FirstScheduledTs    time.Time
	LastScheduledTs     time.Time
	LastBoundTs         time.Time
	GroupReadyTs        time.Time
	TotalSubmitAttempts int
	MaxSubmitAttempts   int
	Censored            bool
}

func (g GroupTimeline) SubmitSpread() time.Duration {
	return g.LastSubmitTs.Sub(g.FirstSubmitTs)
}

func (g GroupTimeline) TimeToReady() time.Duration {
	return g.GroupReadyTs.Sub(g.FirstSubmitTs)
}

// AggregateGroups folds Pod timelines by group ID. Non-gang Pods are excluded.
func AggregateGroups(pods []PodTimeline) []GroupTimeline {
	groups := make(map[string]*GroupTimeline)
	for _, pod := range pods {
		if pod.GroupID == "" {
			continue
		}
		group := groups[pod.GroupID]
		if group == nil {
			group = &GroupTimeline{GroupID: pod.GroupID, MinMember: pod.MinMember}
			groups[pod.GroupID] = group
		}
		group.MemberCount++
		group.TotalSubmitAttempts += pod.Attempts
		if pod.Attempts > group.MaxSubmitAttempts {
			group.MaxSubmitAttempts = pod.Attempts
		}
		group.FirstSubmitTs = minTime(group.FirstSubmitTs, pod.SubmitTs)
		group.LastSubmitTs = maxTime(group.LastSubmitTs, pod.SubmitTs)
		group.FirstScheduledTs = minTime(group.FirstScheduledTs, pod.ScheduledTs)
		group.LastScheduledTs = maxTime(group.LastScheduledTs, pod.ScheduledTs)
		group.LastBoundTs = maxTime(group.LastBoundTs, pod.BoundTs)
		group.GroupReadyTs = maxTime(group.GroupReadyTs, pod.ReadyTs)
		if pod.Censored || pod.ReadyTs.IsZero() {
			group.Censored = true
		}
	}

	out := make([]GroupTimeline, 0, len(groups))
	for _, group := range groups {
		if group.MinMember < 1 || group.MemberCount < group.MinMember {
			group.Censored = true
		}
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FirstSubmitTs.Before(out[j].FirstSubmitTs)
	})
	return out
}

func minTime(a, b time.Time) time.Time {
	if b.IsZero() {
		return a
	}
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// SummarizeGroups reports the mandatory t_submit and t_group_ready distributions.
func SummarizeGroups(groups []GroupTimeline, qs ...float64) Report {
	submitSpread := make([]time.Duration, 0, len(groups))
	timeToReady := make([]time.Duration, 0, len(groups))
	censored := 0
	for _, group := range groups {
		if group.Censored {
			censored++
			continue
		}
		if !group.FirstSubmitTs.IsZero() && !group.LastSubmitTs.IsZero() {
			submitSpread = append(submitSpread, group.SubmitSpread())
		}
		if !group.FirstSubmitTs.IsZero() && !group.GroupReadyTs.IsZero() {
			timeToReady = append(timeToReady, group.TimeToReady())
		}
	}
	total := len(groups)
	rate := 0.0
	if total > 0 {
		rate = float64(censored) / float64(total)
	}
	return Report{
		Total: total, Complete: total - censored, Censored: censored, CensoredRate: rate,
		Phases: []PhaseStats{
			{Name: "t_submit", Count: len(submitSpread), Quantiles: Quantiles(submitSpread, qs...)},
			{Name: "t_group_ready", Count: len(timeToReady), Quantiles: Quantiles(timeToReady, qs...)},
		},
	}
}

func WriteGroupTimelinesCSV(w io.Writer, groups []GroupTimeline) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{
		"group_id", "min_member", "member_count", "first_submit_ts", "last_submit_ts",
		"first_scheduled_ts", "last_scheduled_ts", "last_bound_ts", "group_ready_ts",
		"t_submit_ms", "t_group_ready_ms", "total_submit_attempts", "max_submit_attempts", "censored",
	}); err != nil {
		return fmt.Errorf("write group csv header: %w", err)
	}
	for _, group := range groups {
		if err := cw.Write([]string{
			group.GroupID, fmt.Sprintf("%d", group.MinMember), fmt.Sprintf("%d", group.MemberCount),
			formatTime(group.FirstSubmitTs), formatTime(group.LastSubmitTs),
			formatTime(group.FirstScheduledTs), formatTime(group.LastScheduledTs),
			formatTime(group.LastBoundTs), formatTime(group.GroupReadyTs),
			formatSpan(group.FirstSubmitTs, group.LastSubmitTs),
			formatSpan(group.FirstSubmitTs, group.GroupReadyTs),
			fmt.Sprintf("%d", group.TotalSubmitAttempts), fmt.Sprintf("%d", group.MaxSubmitAttempts),
			fmt.Sprintf("%t", group.Censored),
		}); err != nil {
			return fmt.Errorf("write group csv row: %w", err)
		}
	}
	return cw.Error()
}
