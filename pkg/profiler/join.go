package profiler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// SubmitRecord mirrors one line of the loadgen JSONL (see pkg/loadgen.Record).
// The two structs are kept separate on purpose: the file on disk is the contract
// between the two binaries, not a shared Go type.
type SubmitRecord struct {
	Name     string    `json:"name"`
	UID      string    `json:"uid"`
	SubmitTS time.Time `json:"submit_ts"`
	Attempts int       `json:"attempts"`
}

// JoinStats records what happened during the join, so the run can be reported honestly.
type JoinStats struct {
	Submitted   int // rows read from the submit log
	Matched     int // submitted pods the profiler also observed
	Unobserved  int // submitted but never seen by the watch (missed events / dropped pod)
	Unsubmitted int // observed by the watch but absent from the submit log (foreign pods)
	Censored    int // matched but not terminal by cutoff
	Complete    int // matched and reached Ready
}

// LoadSubmitLog reads the loadgen JSONL. Boilerplate — safe to vibe-code.
// Partial last lines are tolerated only if the file was truncated mid-write; anything
// else is returned as an error so a silently short join can't be mistaken for a good run.
func LoadSubmitLog(r io.Reader) ([]SubmitRecord, error) {
	var out []SubmitRecord
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var rec SubmitRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, fmt.Errorf("submit log line %d: %w", line, err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read submit log: %w", err)
	}
	return out, nil
}

// Join merges submit timestamps into the observed timelines and decides censoring.
//
// MUST HAND-WRITE (Day 3 core #3 and #4: submit_ts join + censored handling).
// Rules to implement, and to justify in the day note:
//   - Join key is UID, not name (GenerateName means the name is only known after
//     creation, and names can be reused across runs).
//   - A pod in the submit log that was never observed is NOT a zero-latency sample and
//     NOT a silently dropped row: it is censored with no known end. Count it.
//   - A pod that was observed but never reached Ready by `cutoff` is right-censored:
//     keep it, set Censored=true, and exclude it from the phases it never completed
//     (it can still contribute to scheduling latency if it did get scheduled).
//   - A pod observed but absent from the submit log has no SubmitTs — it must not enter
//     any submit-relative phase. Count it as Unsubmitted so foreign pods are visible.
//   - Never fabricate a timestamp. Zero time.Time means "unknown", and every consumer
//     downstream must check .IsZero() rather than subtracting blindly.
func Join(submits []SubmitRecord, observed map[string]PodTimeline, cutoff time.Time) ([]PodTimeline, JoinStats) {
	out := make([]PodTimeline, 0, len(submits))
	stats := JoinStats{Submitted: len(submits)}
	submittedUIDs := make(map[string]struct{}, len(submits))

	for _, submit := range submits {
		submittedUIDs[submit.UID] = struct{}{}

		timeline, ok := observed[submit.UID]
		if !ok {
			out = append(out, PodTimeline{
				UID:      submit.UID,
				SubmitTs: submit.SubmitTS,
				Censored: true,
			})
			stats.Unobserved++
			continue
		}

		timeline.UID = submit.UID
		timeline.SubmitTs = submit.SubmitTS
		stats.Matched++

		if timeline.ReadyTs.IsZero() || timeline.ReadyTs.After(cutoff) {
			timeline.Censored = true
			stats.Censored++
		} else {
			timeline.Censored = false
			stats.Complete++
		}
		out = append(out, timeline)
	}

	for uid := range observed {
		if _, ok := submittedUIDs[uid]; !ok {
			stats.Unsubmitted++
		}
	}

	return out, stats
}
