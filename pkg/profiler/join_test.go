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

// TODO(Day3): submitted + observed + Ready => Matched/Complete, SubmitTs filled from
// the log, Censored=false.
func TestJoin_HappyPath(t *testing.T) {
	t.Skip("TODO(Day3): implement Join, then unskip")
}

// TODO(Day3): submitted but never observed => Unobserved, Censored=true, and it must
// still appear in the returned slice (dropping it silently inflates the P99 quality).
func TestJoin_SubmittedButNeverObserved(t *testing.T) {
	t.Skip("TODO(Day3): implement Join, then unskip")
}

// TODO(Day3): observed and scheduled, but not Ready by cutoff => Censored=true, yet
// it still contributes a valid scheduling-latency sample.
func TestJoin_ScheduledButNotReadyAtCutoff(t *testing.T) {
	t.Skip("TODO(Day3): implement Join, then unskip")
}

// TODO(Day3): a pod observed with no matching submit row (leftover from a previous run)
// must be counted as Unsubmitted and must not enter any submit-relative phase.
func TestJoin_ForeignPod(t *testing.T) {
	t.Skip("TODO(Day3): implement Join, then unskip")
}

// TODO(Day3): Summarize reports per-phase Count separately from Total, and excludes
// samples whose endpoints are zero.
func TestSummarize_ExcludesIncompletePhases(t *testing.T) {
	t.Skip("TODO(Day3): implement Summarize, then unskip")
}
