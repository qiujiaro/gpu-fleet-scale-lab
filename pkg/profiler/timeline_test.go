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
	t.Skip("TODO(Day3): implement ExtractState, then unskip")
}

// TODO(Day3): PodScheduled=True must set Scheduled + ScheduledAt from
// LastTransitionTime (not from the observation clock).
func TestExtractState_Scheduled(t *testing.T) {
	t.Skip("TODO(Day3): implement ExtractState, then unskip")
}

// TODO(Day3): a PodScheduled condition with Status=False must NOT count as scheduled.
// This is the bug that quietly makes P99 look great.
func TestExtractState_ScheduledConditionFalse(t *testing.T) {
	t.Skip("TODO(Day3): implement ExtractState, then unskip")
}

// TODO(Day3): the informer re-delivers events; the second Observe of the same pod must
// not move an already-recorded moment. Feed the same PodState twice with a later
// ObservedAt and assert BoundTs is unchanged.
func TestTracker_FirstWriteWins(t *testing.T) {
	t.Skip("TODO(Day3): implement Tracker.Observe, then unskip")
}

// TODO(Day3): a pod first observed already-Ready (profiler started late / resync).
// Decide and assert the policy: backfill from condition timestamps, or refuse the
// sample. Whichever you pick, the test documents it.
func TestTracker_ObservedAlreadyReady(t *testing.T) {
	t.Skip("TODO(Day3): decide the policy, then unskip")
}
