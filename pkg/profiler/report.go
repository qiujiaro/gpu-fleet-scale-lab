package profiler

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"sort"
	"time"
)

// DefaultQuantiles is what Exp1 reports for every phase.
var DefaultQuantiles = []float64{0.5, 0.95, 0.99}

// PhaseStats is one latency phase (scheduling / binding / cold-start / end-to-end).
type PhaseStats struct {
	Name string
	// Count is the number of samples that actually had both endpoints. It is NOT
	// len(timelines): censored pods are excluded per phase, and the gap between
	// Count and Total is the number the reader needs to judge the P99.
	Count     int
	Quantiles map[float64]time.Duration
}

// Report is the per-run summary written to experiments/diagnostics/rejected-pod-latency-baseline/N<k>.csv.
type Report struct {
	Total        int
	Complete     int
	Censored     int
	CensoredRate float64
	Phases       []PhaseStats
}

// Summarize turns joined timelines into per-phase quantiles.
//
// MUST HAND-WRITE (Day 3 core #2 and #3: quantiles + censored handling).
// Quantiles() itself is already written in quantile.go; what is left is the part that
// actually decides what the number means:
//   - Build each phase's sample slice by checking both endpoints with .IsZero() before
//     subtracting. PodTimeline.Scheduling()/Binding()/ColdStart()/EndToEnd() in
//     quantile.go subtract unconditionally, so a zero timestamp yields a nonsense
//     duration — filter first, or add IsZero-safe variants there.
//   - Drop negative durations only after logging them; a negative scheduling latency
//     means the submit_ts join is wrong, and silently clamping hides the bug.
//   - Report Count per phase separately from Total. "P99=120ms over 43 of 500 pods" is
//     an honest sentence; "P99=120ms" alone is not (Day 3 reading question #3).
func Summarize(ts []PodTimeline, qs ...float64) Report {
	samples := [4][]time.Duration{
		make([]time.Duration, 0, len(ts)),
		make([]time.Duration, 0, len(ts)),
		make([]time.Duration, 0, len(ts)),
		make([]time.Duration, 0, len(ts)),
	}
	censored := 0

	add := func(phase string, dst *[]time.Duration, t PodTimeline, from, to time.Time) {
		if from.IsZero() || to.IsZero() {
			return
		}
		d := to.Sub(from)
		if d < 0 {
			log.Printf("profiler: dropping negative %s latency for pod %q: %v", phase, t.UID, d)
			return
		}
		*dst = append(*dst, d)
	}

	for _, t := range ts {
		if t.Censored {
			censored++
		}
		add("scheduling", &samples[0], t, t.SubmitTs, t.ScheduledTs)
		add("binding", &samples[1], t, t.ScheduledTs, t.BoundTs)
		add("cold-start", &samples[2], t, t.BoundTs, t.ReadyTs)
		add("end-to-end", &samples[3], t, t.SubmitTs, t.ReadyTs)
	}

	names := [...]string{"scheduling", "binding", "cold-start", "end-to-end"}
	phases := make([]PhaseStats, len(names))
	for i, name := range names {
		phases[i] = PhaseStats{
			Name:      name,
			Count:     len(samples[i]),
			Quantiles: Quantiles(samples[i], qs...),
		}
	}

	total := len(ts)
	rate := 0.0
	if total != 0 {
		rate = float64(censored) / float64(total)
	}
	return Report{
		Total:        total,
		Complete:     total - censored,
		Censored:     censored,
		CensoredRate: rate,
		Phases:       phases,
	}
}

// WriteTimelinesCSV writes the raw per-pod rows. Boilerplate — safe to vibe-code.
// Unknown timestamps become empty cells rather than 0 or 1970, so downstream pandas
// reads them as NaN instead of a real measurement.
func WriteTimelinesCSV(w io.Writer, ts []PodTimeline) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	header := []string{
		"uid", "name", "group_id", "min_member", "member_index", "attempts",
		"submit_ts", "scheduled_ts", "bound_ts", "ready_ts",
		"scheduling_ms", "binding_ms", "coldstart_ms", "e2e_ms", "censored",
	}
	if err := cw.Write(header); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}
	rows := append([]PodTimeline(nil), ts...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].SubmitTs.Before(rows[j].SubmitTs) })
	for _, t := range rows {
		rec := []string{
			t.UID,
			t.Name,
			t.GroupID,
			fmt.Sprintf("%d", t.MinMember),
			fmt.Sprintf("%d", t.MemberIndex),
			fmt.Sprintf("%d", t.Attempts),
			formatTime(t.SubmitTs),
			formatTime(t.ScheduledTs),
			formatTime(t.BoundTs),
			formatTime(t.ReadyTs),
			formatSpan(t.SubmitTs, t.ScheduledTs),
			formatSpan(t.ScheduledTs, t.BoundTs),
			formatSpan(t.BoundTs, t.ReadyTs),
			formatSpan(t.SubmitTs, t.ReadyTs),
			fmt.Sprintf("%t", t.Censored),
		}
		if err := cw.Write(rec); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}
	return cw.Error()
}

// WriteSummaryCSV writes one row per phase plus the censored accounting.
// Boilerplate — safe to vibe-code.
func WriteSummaryCSV(w io.Writer, r Report) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{
		"phase", "count", "total", "censored", "censored_rate", "p50_ms", "p95_ms", "p99_ms",
	}); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}
	for _, p := range r.Phases {
		if err := cw.Write([]string{
			p.Name,
			fmt.Sprintf("%d", p.Count),
			fmt.Sprintf("%d", r.Total),
			fmt.Sprintf("%d", r.Censored),
			fmt.Sprintf("%.4f", r.CensoredRate),
			formatMillis(p.Quantiles[0.5]),
			formatMillis(p.Quantiles[0.95]),
			formatMillis(p.Quantiles[0.99]),
		}); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}
	return cw.Error()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatSpan(from, to time.Time) string {
	if from.IsZero() || to.IsZero() {
		return ""
	}
	return formatMillis(to.Sub(from))
}

func formatMillis(d time.Duration) string {
	return fmt.Sprintf("%.3f", float64(d)/float64(time.Millisecond))
}
