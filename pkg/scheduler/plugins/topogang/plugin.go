// Package topogang implements a topology-aware gang scheduler plugin.
//
// Hand-written core module #3. Day 4 skeleton; Day 5 fills in the PreFilter/Score/Permit
// algorithms and state machine. Main interview battleground: scheduling vs binding cycle,
// assume consistency, whether Permit blocks the queue, and how gang avoids deadlock.
//
// The interface method signatures below must be verified against the version of
// k8s.io/kubernetes/pkg/scheduler/framework you pin (signatures change across versions on
// pkg.go.dev). This file gives the structure and PodGroup state skeleton first.
package topogang

import (
	"sync"
	"time"
)

// Name is the plugin registration name, matching the KubeSchedulerConfiguration.
const Name = "TopoGang"

// PodGroupState is the gang state machine: Pending -> Collecting -> Ready -> Bound;
// on timeout -> Rejected.
type PodGroupState int

const (
	Pending PodGroupState = iota
	Collecting
	Ready
	Bound
	Rejected
)

// PodGroupInfo is held globally by the plugin; concurrent updates must be locked.
type PodGroupInfo struct {
	UID       string
	MinMember int
	Assigned  int // number of members already assumed/reserved
	Deadline  time.Time
	State     PodGroupState
	mu        sync.Mutex
}

// Registry holds all PodGroup states.
type Registry struct {
	mu     sync.RWMutex
	groups map[string]*PodGroupInfo
}

func NewRegistry() *Registry { return &Registry{groups: map[string]*PodGroupInfo{}} }

// TODO(Day5): implement framework.PreFilterPlugin / ScorePlugin / PermitPlugin / ReservePlugin:
//   PreFilter: check whether the whole group fits; if not, return Unschedulable
//              (avoids partial placement).
//   Filter:    filter out nodes with insufficient GPUs / mismatched topology domain.
//   Score:     the more members of this group already placed in the same nvlink-domain,
//              the higher the score; normalize to [0, MaxNodeScore].
//   Permit:    if minMember not yet gathered, Wait (with timeout); once gathered, Allow all;
//              on timeout, Reject the whole group.
//   Reserve/Unreserve: reserve and roll back, keeping the Assigned count consistent.
//
// New is the framework plugin factory:
//   func New(obj runtime.Object, h framework.Handle) (framework.Plugin, error)
