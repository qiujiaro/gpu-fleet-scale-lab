package profiler

import (
	"testing"
	"time"
)

func TestAggregateGroups(t *testing.T) {
	base := time.Unix(100, 0)
	pods := []PodTimeline{
		{UID: "1", GroupID: "g1", MinMember: 2, Attempts: 1, SubmitTs: base, ScheduledTs: base.Add(time.Second), BoundTs: base.Add(time.Second), ReadyTs: base.Add(3 * time.Second)},
		{UID: "2", GroupID: "g1", MinMember: 2, Attempts: 2, SubmitTs: base.Add(time.Second), ScheduledTs: base.Add(2 * time.Second), BoundTs: base.Add(2 * time.Second), ReadyTs: base.Add(4 * time.Second)},
	}
	got := AggregateGroups(pods)
	if len(got) != 1 {
		t.Fatalf("got %d groups, want 1", len(got))
	}
	group := got[0]
	if group.Censored || group.MemberCount != 2 || group.TotalSubmitAttempts != 3 || group.MaxSubmitAttempts != 2 {
		t.Fatalf("unexpected group: %+v", group)
	}
	if group.SubmitSpread() != time.Second || group.TimeToReady() != 4*time.Second {
		t.Fatalf("unexpected durations: submit=%v ready=%v", group.SubmitSpread(), group.TimeToReady())
	}
}

func TestAggregateGroups_IncompleteIsCensored(t *testing.T) {
	groups := AggregateGroups([]PodTimeline{{
		UID: "1", GroupID: "g1", MinMember: 2, SubmitTs: time.Unix(100, 0),
	}})
	if len(groups) != 1 || !groups[0].Censored {
		t.Fatalf("incomplete group must be censored: %+v", groups)
	}
}
