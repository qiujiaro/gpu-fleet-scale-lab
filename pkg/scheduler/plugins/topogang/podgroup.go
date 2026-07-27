package topogang

import (
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
)

// Label keys used to declare gang membership on a Pod. A real implementation would use a
// PodGroup CRD (as scheduler-plugins does); labels keep Day 4 CRD-free, at the cost of
// having no place to record group-level status.
const (
	LabelPodGroup  = "topogang.dev/pod-group"
	LabelMinMember = "topogang.dev/min-member"
)

// PodGroupState is the gang state machine:
//
//	Pending --(first member reaches Permit)--> Collecting
//	Collecting --(minMember reached)--> Ready --(all bound)--> Bound
//	Collecting --(permit timeout)--> Rejected
//
// Rejected is terminal for this attempt only; the members are requeued and the group is
// re-created from scratch on the next attempt.
type PodGroupState int

const (
	Pending PodGroupState = iota
	Collecting
	Ready
	Bound
	Rejected
)

// PodGroupInfo is the per-gang state. Fields are guarded by Registry's lock, not by a
// per-group lock: taking two locks (registry + group) in a scheduler that also runs
// Permit callbacks is how you get a lock-ordering deadlock, and the groups map is small
// enough that one lock costs nothing.
type PodGroupInfo struct {
	UID       string
	MinMember int

	// Assigned counts members that have passed Reserve and not been Unreserved.
	// Reserve/Unreserve must keep this exact — see the note in plugin.go.
	Assigned int

	// PlacedByDomain counts assigned members per topology domain; this is the input to
	// the Day 5 locality Score.
	PlacedByDomain map[string]int

	Deadline time.Time
	State    PodGroupState
}

// Registry holds all PodGroup states for the lifetime of the scheduler process.
type Registry struct {
	mu     sync.Mutex
	groups map[string]*PodGroupInfo
}

func NewRegistry() *Registry { return &Registry{groups: map[string]*PodGroupInfo{}} }

// PodGroupOf reads the group UID and minMember off a Pod's labels.
// ok is false for a Pod that is not part of any gang — such Pods must be scheduled
// normally, never blocked in Permit.
//
// MUST HAND-WRITE is *not* claimed here: this is label-parsing boilerplate (guide §7,
// "可 vibe coding"). The parts that matter are in plugin.go and in the methods below.
func PodGroupOf(pod *v1.Pod) (uid string, minMember int, ok bool) {
	// TODO(Day5): parse LabelPodGroup / LabelMinMember, namespace-qualify the UID
	// (group names are only unique within a namespace), reject minMember < 1.
	return "", 0, false
}

// Get returns the group state, creating it on first sight.
//
// MUST HAND-WRITE (Day 5). The creation path is where minMember and Deadline get their
// values, and where a second member arriving concurrently must find the *same* struct —
// check-then-create under a single held lock, not two calls.
func (r *Registry) Get(uid string, minMember int, deadline time.Time) *PodGroupInfo {
	// TODO(Day5): lock, look up, create-if-absent, return.
	return nil
}

// Assign records that one member reserved nodeName in domain, and reports the new
// assigned count.
//
// MUST HAND-WRITE (Day 5). Called from Reserve.
func (r *Registry) Assign(uid, domain string) int {
	// TODO(Day5): increment Assigned and PlacedByDomain[domain]; transition
	// Collecting -> Ready when Assigned >= MinMember.
	return 0
}

// Release undoes one Assign. Must be idempotent and must never drive Assigned below
// zero — Unreserve is called for Pods that never reserved.
//
// MUST HAND-WRITE (Day 5). Called from Unreserve.
func (r *Registry) Release(uid, domain string) {
	// TODO(Day5): decrement with a floor at 0; drop the group when it hits 0.
}

// PlacedInDomain reports how many members of this group are already placed in domain.
// This is the Day 5 Score input.
//
// MUST HAND-WRITE (Day 5).
func (r *Registry) PlacedInDomain(uid, domain string) int {
	// TODO(Day5): read under lock.
	return 0
}
