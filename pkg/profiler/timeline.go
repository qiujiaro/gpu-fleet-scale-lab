package profiler

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// PodState is the informer-independent projection of one Pod observation.
//
// Keeping this type free of client-go lets Tracker be unit-tested without a cluster:
// tests feed PodState values directly, the informer path only has to build them.
type PodState struct {
	UID  string
	Name string

	// ObservedAt is the client-side wall clock when this event came out of the watch.
	// The delta between ObservedAt and the server-side condition timestamps *is* the
	// watch propagation latency (Day 3 reading question #1).
	ObservedAt time.Time

	// Scheduled records the server-side PodScheduled condition for diagnostics only.
	// ScheduledAt is intentionally not used as the latency clock: Kubernetes condition
	// timestamps have only second-level precision in the environments profiled here.
	Scheduled   bool
	ScheduledAt time.Time

	// NodeName is spec.nodeName; non-empty means the binding has been written to etcd.
	NodeName string

	// Ready is true once the Ready condition is True. ReadyAt preserves the condition's
	// server timestamp for diagnostics only; Tracker uses ObservedAt as the latency
	// clock because condition timestamps are too coarse for kwok-speed measurements.
	Ready   bool
	ReadyAt time.Time

	Phase corev1.PodPhase
}

// ExtractState projects a Pod object into a PodState.
//
// MUST HAND-WRITE (Day 3 core #1: four-moment extraction).
// Decisions to make explicitly, and to defend in docs/notes/logs/day3-profiler.md:
//   - Which field marks "scheduled": the PodScheduled condition (server timestamp, but
//     only second-granularity) or the first observation of spec.nodeName (client
//     timestamp, millisecond, but includes watch propagation delay)?
//   - Which field marks "bound" as distinct from "scheduled"? Suggested split:
//     ScheduledTs = PodScheduled condition LastTransitionTime (server side),
//     BoundTs     = ObservedAt of the first event where spec.nodeName != "" (client side).
//     Then BoundTs-ScheduledTs is your measured watch propagation, which is exactly the
//     quantity you cross-check against Prometheus.
//   - Ready: the Ready condition, not phase==Running. Under kwok both are faked by the
//     kwok controller — write down in the note what this does and does not measure.
//
// API reference:
//   - PodStatus / PodCondition / PodConditionType:
//     https://pkg.go.dev/k8s.io/api/core/v1#PodStatus
//     https://pkg.go.dev/k8s.io/api/core/v1#PodCondition
//   - Pod lifecycle & condition semantics:
//     https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-conditions
func ExtractState(pod *corev1.Pod, observedAt time.Time) PodState {
	// TODO(Day3): fill Scheduled/ScheduledAt from the PodScheduled condition,
	// NodeName from pod.Spec.NodeName, Ready/ReadyAt from the Ready condition.

	podState := PodState{
		UID:         string(pod.UID),
		Name:        pod.Name,
		ObservedAt:  observedAt,
		Phase:       pod.Status.Phase,
		NodeName:    pod.Spec.NodeName,
		Scheduled:   false,
		ScheduledAt: time.Time{},
		Ready:       false,
		ReadyAt:     time.Time{},
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionTrue {
			podState.Scheduled = true
			podState.ScheduledAt = condition.LastTransitionTime.Time

		} else if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			podState.Ready = condition.Status == corev1.ConditionTrue
			podState.ReadyAt = condition.LastTransitionTime.Time
		}
	}
	return podState
}

// Tracker accumulates the per-Pod timeline while the informer is running.
// It is written to from informer handler goroutines and read at the end of the run,
// so every method must be safe for concurrent use.
type Tracker struct {
	mu        sync.Mutex
	timelines map[string]*PodTimeline // key: pod UID (matches loadgen's Record.UID)
}

func NewTracker() *Tracker {
	return &Tracker{timelines: make(map[string]*PodTimeline)}
}

// Observe folds one observation into the timeline for that Pod.
//
// MUST HAND-WRITE (Day 3 core #1). Rules to implement deliberately:
//   - First-write-wins per moment: informers re-deliver and resync, so a later event
//     must never overwrite an already-recorded ScheduledTs/BoundTs/ReadyTs.
//   - Out-of-order safety: a pod can be observed already-Ready on the very first event
//     (profiler started late, or resync). Decide whether to backfill the earlier moments
//     from server-side condition timestamps or to mark the sample unusable.
//   - Never record SubmitTs here — it comes from the loadgen JSONL in Join.
func (t *Tracker) Observe(s PodState) {
	// TODO(Day3): implement the state machine described above.
	t.mu.Lock()
	defer t.mu.Unlock()
	tl, ok := t.timelines[s.UID]
	if !ok {
		tl = &PodTimeline{
			UID: s.UID,
		}
		t.timelines[s.UID] = tl
	}
	if s.NodeName != "" {
		// First observation of spec.nodeName is the only sub-second, client-side clock
		// shared by every run. Use it for both scheduling completion and observed bind;
		// framework metrics provide the internal t_cycle/t_permit/t_bind split.
		if tl.ScheduledTs.IsZero() {
			tl.ScheduledTs = s.ObservedAt
		}
		if tl.BoundTs.IsZero() {
			tl.BoundTs = s.ObservedAt
		}
	}
	if s.Ready && tl.ReadyTs.IsZero() {
		tl.ReadyTs = s.ObservedAt
	}

}

// Snapshot returns a copy of everything observed so far, keyed by UID.
// Safe to call while the informer is still running.
func (t *Tracker) Snapshot() map[string]PodTimeline {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]PodTimeline, len(t.timelines))
	for uid, tl := range t.timelines {
		out[uid] = *tl
	}
	return out
}

// Len reports how many distinct pods have been observed.
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.timelines)
}
