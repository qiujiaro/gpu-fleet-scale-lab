package topogang

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
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

// PodReservation is TopoGang's bookkeeping for one Pod that passed Reserve. Kubernetes'
// scheduler cache owns the real assumed-resource accounting; this record only makes the
// gang count idempotent and supplies topology placement data to Score.
type PodReservation struct {
	PodUID     types.UID
	NodeName   string
	Domain     string
	ReservedAt time.Time
}

// PodGroupInfo is the per-gang state. Fields are guarded by Registry's lock, not by a
// per-group lock: taking two locks (registry + group) in a scheduler that also runs
// Permit callbacks is how you get a lock-ordering deadlock, and the groups map is small
// enough that one lock costs nothing.
type PodGroupInfo struct {
	UID       string
	MinMember int

	// Reservations is keyed by Pod UID so duplicate Reserve and Unreserve calls are
	// idempotent. The assigned member count is always len(Reservations); do not add a
	// second integer counter that can drift away from this source of truth.
	Reservations map[types.UID]PodReservation

	// PlacedByDomain counts assigned members per topology domain; this is the input to
	// the Day 5 locality Score. Reservations on nodes without the configured topology
	// label are deliberately omitted so missing labels do not form a fake shared domain.
	PlacedByDomain map[string]int

	Deadline time.Time
	State    PodGroupState

	// AttemptID distinguishes a newly-created attempt from stale timeout callbacks for
	// an older attempt that used the same namespace/name.
	AttemptID    uint64
	TimerStarted bool
}

// Registry holds all PodGroup states for the lifetime of the scheduler process.
type Registry struct {
	mu          sync.Mutex
	groups      map[string]*PodGroupInfo
	nextAttempt uint64
}

func NewRegistry() *Registry { return &Registry{groups: map[string]*PodGroupInfo{}} }

func (r *Registry) lock() {
	start := time.Now()
	r.mu.Lock()
	observeRegistryLock(start)
}

// PodGroupOf reads the group UID and minMember off a Pod's labels.
// ok is false for a Pod that is not part of any gang — such Pods must be scheduled
// normally, never blocked in Permit.
//
// MUST HAND-WRITE is *not* claimed here: this is label-parsing boilerplate (guide §7,
// "可 vibe coding"). The parts that matter are in plugin.go and in the methods below.
func PodGroupOf(pod *v1.Pod) (uid string, minMember int, ok bool) {
	uid, minMember, ok, _ = parsePodGroup(pod)
	return uid, minMember, ok
}

// parsePodGroup distinguishes a normal non-gang Pod from a malformed gang Pod. The
// public PodGroupOf helper keeps its compact tuple for callers that only need membership;
// PreFilter uses this stricter form so bad labels cannot silently disable gang semantics.
func parsePodGroup(pod *v1.Pod) (uid string, minMember int, ok bool, err error) {
	if pod == nil {
		return "", 0, false, fmt.Errorf("pod is nil")
	}

	groupName, hasGroup := pod.Labels[LabelPodGroup]
	minMemberText, hasMinMember := pod.Labels[LabelMinMember]
	if !hasGroup && !hasMinMember {
		return "", 0, false, nil
	}
	if !hasGroup || groupName == "" {
		return "", 0, false, fmt.Errorf("label %q must be set to a non-empty value", LabelPodGroup)
	}
	if !hasMinMember {
		return "", 0, false, fmt.Errorf("label %q is required for pod group %q", LabelMinMember, groupName)
	}

	minMember, err = strconv.Atoi(minMemberText)
	if err != nil || minMember < 1 {
		return "", 0, false, fmt.Errorf("label %q must be a positive integer", LabelMinMember)
	}
	return pod.Namespace + "/" + groupName, minMember, true, nil
}

// PodGroupSnapshot is an immutable copy of the fields an extension point may inspect
// after Registry releases its lock. Maps are deliberately excluded.
type PodGroupSnapshot struct {
	UID             string
	MinMember       int
	ReservedMembers int
	Deadline        time.Time
	State           PodGroupState
	AttemptID       uint64
}

// Ensure returns a safe group snapshot, creating the group on first sight. A conflicting
// minMember is rejected atomically instead of allowing different Pods to describe the
// same gang differently.
func (r *Registry) Ensure(uid string, minMember int, deadline time.Time) (PodGroupSnapshot, error) {
	r.lock()
	defer r.mu.Unlock()

	group, ok := r.groups[uid]
	if !ok {
		r.nextAttempt++
		group = &PodGroupInfo{
			UID:            uid,
			MinMember:      minMember,
			Reservations:   make(map[types.UID]PodReservation),
			PlacedByDomain: make(map[string]int),
			Deadline:       deadline,
			State:          Pending,
			AttemptID:      r.nextAttempt,
		}
		r.groups[uid] = group
	} else if group.MinMember != minMember {
		return PodGroupSnapshot{}, fmt.Errorf(
			"pod group %q has conflicting minMember values %d and %d",
			uid, group.MinMember, minMember,
		)
	}

	return PodGroupSnapshot{
		UID:             group.UID,
		MinMember:       group.MinMember,
		ReservedMembers: len(group.Reservations),
		Deadline:        group.Deadline,
		State:           group.State,
		AttemptID:       group.AttemptID,
	}, nil
}

// Get returns the group state, creating it on first sight.
//
// MUST HAND-WRITE (Day 5). The creation path is where minMember and Deadline get their
// values, and where a second member arriving concurrently must find the *same* struct —
// check-then-create under a single held lock, not two calls.
func (r *Registry) Get(uid string, minMember int, deadline time.Time) *PodGroupInfo {
	r.lock()
	defer r.mu.Unlock()

	if group, ok := r.groups[uid]; ok {
		return group
	}

	r.nextAttempt++
	group := &PodGroupInfo{
		UID:            uid,
		MinMember:      minMember,
		Reservations:   make(map[types.UID]PodReservation),
		PlacedByDomain: make(map[string]int),
		Deadline:       deadline,
		State:          Pending,
		AttemptID:      r.nextAttempt,
	}
	r.groups[uid] = group
	return group
}

// Assign records that one member reserved nodeName in domain and reports the number of
// distinct reserved Pods. Repeating the same reservation is a no-op; moving the same Pod
// without an intervening Release is a state error.
//
// MUST HAND-WRITE (Day 5). Called from Reserve.
func (r *Registry) Assign(uid string, podUID types.UID, nodeName, domain string, reservedAt time.Time) (int, error) {
	r.lock()
	defer r.mu.Unlock()

	group, ok := r.groups[uid]
	if !ok {
		return 0, fmt.Errorf("pod group %q not found", uid)
	}
	if group.State == Rejected {
		return len(group.Reservations), fmt.Errorf("pod group %q is rejected", uid)
	}

	if existing, ok := group.Reservations[podUID]; ok {
		if existing.NodeName != nodeName || existing.Domain != domain {
			return len(group.Reservations), fmt.Errorf(
				"pod %q is already reserved on node %q in domain %q",
				podUID, existing.NodeName, existing.Domain,
			)
		}
		return len(group.Reservations), nil
	}

	group.Reservations[podUID] = PodReservation{
		PodUID:     podUID,
		NodeName:   nodeName,
		Domain:     domain,
		ReservedAt: reservedAt,
	}
	if domain != "" {
		group.PlacedByDomain[domain]++
	}

	assigned := len(group.Reservations)
	if assigned >= group.MinMember {
		group.State = Ready
	} else {
		group.State = Collecting
	}
	return assigned, nil
}

// Release undoes one Assign by Pod UID. A missing group or reservation is a no-op, which
// makes Unreserve safe to call repeatedly or for a Pod that never reserved.
//
// MUST HAND-WRITE (Day 5). Called from Unreserve.
func (r *Registry) Release(uid string, podUID types.UID) {
	r.lock()
	defer r.mu.Unlock()

	group, ok := r.groups[uid]
	if !ok {
		return
	}
	reservation, ok := group.Reservations[podUID]
	if !ok {
		return
	}

	delete(group.Reservations, podUID)
	if reservation.Domain != "" {
		if count := group.PlacedByDomain[reservation.Domain]; count <= 1 {
			delete(group.PlacedByDomain, reservation.Domain)
		} else {
			group.PlacedByDomain[reservation.Domain] = count - 1
		}
	}

	assigned := len(group.Reservations)
	if assigned == 0 {
		delete(r.groups, uid)
		return
	}
	if group.State != Rejected && assigned < group.MinMember {
		group.State = Collecting
	}
}

// PlacedInDomain reports how many members of this group are already placed in domain.
// This is the Day 5 Score input.
//
// MUST HAND-WRITE (Day 5).
func (r *Registry) PlacedInDomain(uid, domain string) int {
	if domain == "" {
		return 0
	}
	r.lock()
	defer r.mu.Unlock()

	group, ok := r.groups[uid]
	if !ok {
		return 0
	}
	return group.PlacedByDomain[domain]
}

// PermitDecision is an immutable result of checking a gang at the Permit barrier.
type PermitDecision struct {
	Ready       bool
	Rejected    bool
	Deadline    time.Time
	AttemptID   uint64
	StartTimer  bool
	WaitStarted time.Time
}

// DecidePermit atomically observes the current reservation count and advances the group
// state. Exactly one waiter is asked to start the group deadline timer.
func (r *Registry) DecidePermit(uid string, now time.Time) (PermitDecision, error) {
	r.lock()
	defer r.mu.Unlock()

	group, ok := r.groups[uid]
	if !ok {
		return PermitDecision{}, fmt.Errorf("pod group %q not found", uid)
	}

	decision := PermitDecision{
		Deadline:    group.Deadline,
		AttemptID:   group.AttemptID,
		WaitStarted: earliestReservation(group),
	}
	if group.State == Rejected {
		decision.Rejected = true
		return decision, nil
	}
	if len(group.Reservations) >= group.MinMember {
		group.State = Ready
		decision.Ready = true
		return decision, nil
	}
	if !now.Before(group.Deadline) {
		group.State = Rejected
		decision.Rejected = true
		return decision, nil
	}

	group.State = Collecting
	if !group.TimerStarted {
		group.TimerStarted = true
		decision.StartTimer = true
	}
	return decision, nil
}

func earliestReservation(group *PodGroupInfo) time.Time {
	var earliest time.Time
	for _, reservation := range group.Reservations {
		if earliest.IsZero() || reservation.ReservedAt.Before(earliest) {
			earliest = reservation.ReservedAt
		}
	}
	return earliest
}

// RejectIfCollecting rejects only the attempt that scheduled the timer. It is safe for
// an old timer to fire after its group was deleted and recreated with the same UID.
func (r *Registry) RejectIfCollecting(uid string, attemptID uint64, now time.Time) bool {
	r.lock()
	defer r.mu.Unlock()

	group, ok := r.groups[uid]
	if !ok || group.AttemptID != attemptID || group.State != Collecting || now.Before(group.Deadline) {
		return false
	}
	group.State = Rejected
	return true
}
