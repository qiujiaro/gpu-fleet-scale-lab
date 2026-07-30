package profiler

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// podAt builds a Pod fixture. scheduledAt/readyAt zero means the condition is absent.
func podAt(uid, nodeName string, scheduledAt, readyAt time.Time) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p-" + uid, UID: types.UID(uid)},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	if !scheduledAt.IsZero() {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
			Type:               corev1.PodScheduled,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(scheduledAt),
		})
	}
	if !readyAt.IsZero() {
		p.Status.Phase = corev1.PodRunning
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(readyAt),
		})
	}
	return p
}

// TODO(Day3): a pending pod with no conditions yields Scheduled=false, Ready=false,
// empty NodeName, and ObservedAt set to the passed clock.
func TestExtractState_Pending(t *testing.T) {
	observedAt := time.Unix(10, 123)
	got := ExtractState(podAt("1", "", time.Time{}, time.Time{}), observedAt)
	if got.Scheduled || got.Ready || got.NodeName != "" || !got.ObservedAt.Equal(observedAt) {
		t.Fatalf("unexpected pending state: %+v", got)
	}
}

// TODO(Day3): PodScheduled=True must set Scheduled + ScheduledAt from
// LastTransitionTime (not from the observation clock).
func TestExtractState_Scheduled(t *testing.T) {
	scheduledAt := time.Unix(10, 0)
	got := ExtractState(podAt("1", "node-1", scheduledAt, time.Time{}), time.Unix(11, 123))
	if !got.Scheduled || !got.ScheduledAt.Equal(scheduledAt) || got.NodeName != "node-1" {
		t.Fatalf("unexpected scheduled state: %+v", got)
	}
}

// TODO(Day3): a PodScheduled condition with Status=False must NOT count as scheduled.
// This is the bug that quietly makes P99 look great.
func TestExtractState_ScheduledConditionFalse(t *testing.T) {
	pod := podAt("1", "", time.Time{}, time.Time{})
	pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
		LastTransitionTime: metav1.NewTime(time.Unix(10, 0)),
	})
	got := ExtractState(pod, time.Unix(11, 0))
	if got.Scheduled || !got.ScheduledAt.IsZero() {
		t.Fatalf("false condition counted as scheduled: %+v", got)
	}
}

// TODO(Day3): the informer re-delivers events; the second Observe of the same pod must
// not move an already-recorded moment. Feed the same PodState twice with a later
// ObservedAt and assert BoundTs is unchanged.
func TestTracker_FirstWriteWins(t *testing.T) {
	tracker := NewTracker()
	first := time.Unix(10, 100)
	tracker.Observe(PodState{UID: "1", NodeName: "node-1", ObservedAt: first})
	tracker.Observe(PodState{UID: "1", NodeName: "node-1", ObservedAt: first.Add(time.Second)})
	got := tracker.Snapshot()["1"]
	if !got.ScheduledTs.Equal(first) || !got.BoundTs.Equal(first) {
		t.Fatalf("first observation was overwritten: %+v", got)
	}
}

// TODO(Day3): a pod first observed already-Ready (profiler started late / resync).
// Decide and assert the policy: backfill from condition timestamps, or refuse the
// sample. Whichever you pick, the test documents it.
func TestTracker_ObservedAlreadyReady(t *testing.T) {
	tracker := NewTracker()
	observedAt := time.Unix(12, 500)
	readyAt := time.Unix(12, 0)
	tracker.Observe(ExtractState(podAt("1", "node-1", time.Unix(11, 0), readyAt), observedAt))
	got := tracker.Snapshot()["1"]
	if !got.ScheduledTs.Equal(observedAt) || !got.BoundTs.Equal(observedAt) || !got.ReadyTs.Equal(observedAt) {
		t.Fatalf("unexpected already-ready policy: %+v", got)
	}
}

func TestTracker_ReadyUsesFirstObservationClock(t *testing.T) {
	tracker := NewTracker()
	first := time.Unix(20, 123)
	tracker.Observe(PodState{
		UID: "1", NodeName: "node-1", ObservedAt: first,
		Ready: true, ReadyAt: time.Unix(20, 0),
	})
	tracker.Observe(PodState{
		UID: "1", NodeName: "node-1", ObservedAt: first.Add(time.Second),
		Ready: true, ReadyAt: time.Unix(21, 0),
	})
	got := tracker.Snapshot()["1"]
	if !got.ReadyTs.Equal(first) {
		t.Fatalf("ReadyTs=%v, want first observation %v", got.ReadyTs, first)
	}
}
