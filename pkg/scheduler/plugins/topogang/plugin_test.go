package topogang

import (
	"context"
	"testing"
)

// TestNew_NeutralSkeleton is the Day 4 acceptance test in unit form: the factory builds,
// the plugin names itself consistently, and every extension point returns a neutral
// value. It is NOT skipped — it must pass on Day 4.
func TestNew_NeutralSkeleton(t *testing.T) {
	// Handle is nil on purpose: New must not dereference it, otherwise the plugin
	// cannot be constructed before informers exist.
	p, err := New(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != Name {
		t.Fatalf("Name() = %q, want %q", p.Name(), Name)
	}

	tg := p.(*TopoGang)
	if _, st := tg.PreFilter(context.Background(), nil, nil); !st.IsSuccess() {
		t.Errorf("neutral PreFilter returned %v", st)
	}
	if st := tg.Filter(context.Background(), nil, nil, nil); !st.IsSuccess() {
		t.Errorf("neutral Filter returned %v", st)
	}
	score, st := tg.Score(context.Background(), nil, nil, "node-1")
	if !st.IsSuccess() {
		t.Errorf("neutral Score returned %v", st)
	}
	other, _ := tg.Score(context.Background(), nil, nil, "node-2")
	if score != other {
		t.Errorf("neutral Score is not constant: node-1=%d node-2=%d", score, other)
	}
	if st, wait := tg.Permit(context.Background(), nil, nil, "node-1"); !st.IsSuccess() || wait != 0 {
		t.Errorf("neutral Permit returned (%v, %v), want (Success, 0)", st, wait)
	}
}

// TODO(Day5): a Pod with no gang labels yields ok=false, and such a Pod must never be
// held in Permit. This is the "one non-gang Pod deadlocks the cluster" test.
func TestPodGroupOf_NoLabels(t *testing.T) {
	t.Skip("TODO(Day5): implement PodGroupOf, then unskip")
}

// TODO(Day5): minMember must parse from the label and reject <1 and non-numeric values.
// minMember=0 means Permit's "gathered" check is true before any member arrives.
func TestPodGroupOf_MinMember(t *testing.T) {
	t.Skip("TODO(Day5): implement PodGroupOf, then unskip")
}

// TODO(Day5): two goroutines calling Get for the same UID concurrently must receive the
// *same* *PodGroupInfo. Run with -race; a check-then-create split across two lock
// acquisitions fails here.
func TestRegistry_GetIsSingleton(t *testing.T) {
	t.Skip("TODO(Day5): implement Registry.Get, then unskip")
}

// TODO(Day5): Assign then Release returns to zero; a second Release must not go negative
// (Unreserve is called for Pods that never reserved).
func TestRegistry_ReleaseIsIdempotent(t *testing.T) {
	t.Skip("TODO(Day5): implement Assign/Release, then unskip")
}

// TODO(Day5): Score must land in [framework.MinNodeScore, framework.MaxNodeScore] for
// every placed-count from 0 to maxGroupSize, including placed > maxGroupSize.
func TestScore_Normalized(t *testing.T) {
	t.Skip("TODO(Day5): implement Score, then unskip")
}

// TODO(Day5): the minMember-th member's Permit returns Success and releases its waiting
// siblings; earlier members return Wait with a positive timeout.
func TestPermit_WaitsUntilMinMember(t *testing.T) {
	t.Skip("TODO(Day5): implement Permit, then unskip")
}

// TODO(Day5): on timeout the whole group is rejected, not just the late member, and
// Unreserve drives Assigned back to zero.
func TestPermit_TimeoutRejectsWholeGroup(t *testing.T) {
	t.Skip("TODO(Day5): implement the Permit timeout path, then unskip")
}
