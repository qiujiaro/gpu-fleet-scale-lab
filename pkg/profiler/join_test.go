package profiler

import (
	"strings"
	"testing"
	"time"
)

func TestLoadSubmitLog(t *testing.T) {
	in := `{"name":"a","uid":"u1","submit_ts":"2026-07-24T10:00:00Z","attempts":1}
{"name":"b","uid":"u2","submit_ts":"2026-07-24T10:00:01Z","attempts":2}
`
	got, err := LoadSubmitLog(strings.NewReader(in))
	if err != nil {
		t.Fatalf("LoadSubmitLog: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %d", len(got))
	}
	if got[1].UID != "u2" || got[1].Attempts != 2 {
		t.Errorf("unexpected record: %+v", got[1])
	}
	if !got[0].SubmitTS.Equal(time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("submit_ts parsed wrong: %v", got[0].SubmitTS)
	}
}

func TestLoadSubmitLog_MalformedLineIsAnError(t *testing.T) {
	if _, err := LoadSubmitLog(strings.NewReader("{not json}\n")); err == nil {
		t.Fatal("want error on malformed line, got nil")
	}
}

func TestJoin_HappyPath(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	submits := []SubmitRecord{{Name: "pod-a", UID: "u1", SubmitTS: base, Attempts: 1}}
	observed := map[string]PodTimeline{
		"u1": {UID: "u1", ScheduledTs: base.Add(time.Second), BoundTs: base.Add(2 * time.Second), ReadyTs: base.Add(3 * time.Second)},
	}
	got, stats := Join(submits, observed, base.Add(4*time.Second))
	if len(got) != 1 || got[0].Censored || !got[0].SubmitTs.Equal(base) {
		t.Fatalf("unexpected joined timeline: %+v", got)
	}
	if stats != (JoinStats{Submitted: 1, Matched: 1, Complete: 1}) {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestJoin_SubmittedButNeverObserved(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	got, stats := Join([]SubmitRecord{{Name: "pod-a", UID: "u1", SubmitTS: base}}, nil, base.Add(time.Minute))
	if len(got) != 1 || !got[0].Censored || got[0].UID != "u1" {
		t.Fatalf("unobserved submit was not retained as censored: %+v", got)
	}
	if stats.Submitted != 1 || stats.Unobserved != 1 || stats.Matched != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestJoin_ScheduledButNotReadyAtCutoff(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	submits := []SubmitRecord{{Name: "pod-a", UID: "u1", SubmitTS: base}}
	observed := map[string]PodTimeline{"u1": {UID: "u1", ScheduledTs: base.Add(time.Second)}}
	got, stats := Join(submits, observed, base.Add(time.Minute))
	report := Summarize(got, 0.5)
	if !got[0].Censored || stats.Censored != 1 || stats.Matched != 1 {
		t.Fatalf("unexpected censored join: timeline=%+v stats=%+v", got[0], stats)
	}
	if report.Phases[0].Count != 1 || report.Phases[3].Count != 0 {
		t.Fatalf("scheduled sample should survive while e2e is excluded: %+v", report.Phases)
	}
}

func TestJoin_ForeignPod(t *testing.T) {
	observed := map[string]PodTimeline{"foreign": {UID: "foreign", ReadyTs: time.Now()}}
	got, stats := Join(nil, observed, time.Now())
	if len(got) != 0 || stats.Unsubmitted != 1 || stats.Submitted != 0 {
		t.Fatalf("foreign pod entered joined output: timelines=%+v stats=%+v", got, stats)
	}
}

func TestSummarize_ExcludesIncompletePhases(t *testing.T) {
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	input := []PodTimeline{
		{UID: "complete", SubmitTs: base, ScheduledTs: base.Add(time.Second), BoundTs: base.Add(2 * time.Second), ReadyTs: base.Add(3 * time.Second)},
		{UID: "scheduled-only", SubmitTs: base, ScheduledTs: base.Add(4 * time.Second), Censored: true},
	}
	got := Summarize(input, 0.5)
	if got.Total != 2 || got.Complete != 1 || got.Censored != 1 || got.CensoredRate != 0.5 {
		t.Fatalf("unexpected report totals: %+v", got)
	}
	wantCounts := []int{2, 1, 1, 1}
	for i, want := range wantCounts {
		if got.Phases[i].Count != want {
			t.Errorf("phase %q count=%d, want %d", got.Phases[i].Name, got.Phases[i].Count, want)
		}
	}
}
