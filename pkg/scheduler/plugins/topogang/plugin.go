// Package topogang implements a topology-aware gang scheduler plugin.
//
// Hand-written core module #3. Day 4 provided the neutral scheduler skeleton; Day 5 adds
// whole-group GPU admission, per-node filtering, topology-aware scoring, idempotent
// reservations, and the Permit gang barrier.
//
// Interface signatures are pinned to k8s.io/kubernetes v1.30.0 — they drift across
// releases (Score gained CycleState, PreFilter gained *PreFilterResult, the factory
// gained ctx), so verify on pkg.go.dev for the version in go.mod before editing.
package topogang

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

// Name is the plugin registration name. It must match three places or the plugin is
// silently never invoked:
//   - app.WithPlugin(topogang.Name, topogang.New) in cmd/scheduler/main.go
//   - profiles[].plugins.<extensionPoint>.enabled[].name in the KubeSchedulerConfiguration
//   - profiles[].pluginConfig[].name, if the plugin takes args
//
// Note this is NOT the schedulerName: the profile name (`topogang`, lowercase) is what
// Pods put in spec.schedulerName. Same word, different namespace of meaning.
const Name = "TopoGang"

const preFilterStateKey framework.StateKey = Name + "/pre-filter-state"

// TopoGang is the plugin instance. The framework constructs exactly one per profile and
// shares it across all scheduling goroutines, so every field reachable from an extension
// point must be safe for concurrent use — hence the mutex inside Registry.
type TopoGang struct {
	handle framework.Handle
	args   TopoGangArgs

	// groups is the cross-Pod gang state. It lives on the plugin, not in CycleState:
	// CycleState is per-Pod per-cycle and is discarded at the end of the cycle, while
	// gang membership by definition spans Pods and cycles.
	groups *Registry
}

type PreFilterState struct {
	GroupUID      string
	MinMember     int
	PodGPURequest int64
}

// Clone makes PreFilterState valid framework.StateData. All fields are value types, so
// a shallow copy is sufficient and avoids sharing mutable state between cycle clones.
func (s *PreFilterState) Clone() framework.StateData {
	if s == nil {
		return nil
	}
	clone := *s
	return &clone
}

// Compile-time proof that the skeleton actually satisfies the extension points it claims.
// Without these, a signature typo produces a plugin that registers fine and is never
// called — the failure mode is silence, not a compile error.
var (
	_ framework.PreFilterPlugin = &TopoGang{}
	_ framework.FilterPlugin    = &TopoGang{}
	_ framework.ScorePlugin     = &TopoGang{}
	_ framework.ReservePlugin   = &TopoGang{}
	_ framework.PermitPlugin    = &TopoGang{}
)

// New is the framework plugin factory, matching runtime.PluginFactory for v1.30.0.
//
// MUST HAND-WRITE (Day 4 core). Decisions to make explicitly:
//   - `obj` is the decoded pluginConfig[].args for this plugin, or nil when the profile
//     provides none. Day 4 may ignore it; Day 5 wants at least permitTimeout and
//     the topology label key, which means a registered args type (see args.go on Day 5).
//   - `h` (framework.Handle) is the only door back to cluster state: h.SnapshotSharedLister()
//     for the node snapshot, h.ClientSet()/h.SharedInformerFactory() for live reads,
//     h.IterateOverWaitingPods()/h.GetWaitingPod() for the Permit gang release on Day 5.
//     Keep the handle; you cannot recover it later.
//   - Returning an error here aborts scheduler startup. That is the right behaviour for
//     a malformed config — do not fall back to defaults silently.
func New(_ context.Context, obj runtime.Object, h framework.Handle) (framework.Plugin, error) {
	registerMetrics()
	args := defaultTopoGangArgs()
	if err := frameworkruntime.DecodeInto(obj, &args); err != nil {
		return nil, err
	}
	if err := args.validate(); err != nil {
		return nil, err
	}
	return &TopoGang{handle: h, args: args, groups: NewRegistry()}, nil
}

// Name returns the plugin name. Part of framework.Plugin.
func (p *TopoGang) Name() string { return Name }

// PreFilter, Filter, Score, and NormalizeScore choose a node. Once the scheduler assumes
// that choice in its cache, Reserve and Permit run on the path toward an asynchronous
// binding cycle. Returning Wait from Permit parks only this Pod's binding.

// PreFilter runs once per Pod, before any node is considered.
//
// MUST HAND-WRITE (Day 5 core). Day 4 keeps it neutral.
// Rules to implement on Day 5:
//   - Read the PodGroup UID and minMember off the Pod (label/annotation) into CycleState
//     so Filter/Score/Permit do not re-parse them per node.
//   - Whole-group capacity check: if the cluster cannot hold minMember members, return
//     framework.NewStatus(framework.Unschedulable, ...) here rather than letting members
//     trickle in and hold resources — that is the entire point of gang.
//   - The returned *PreFilterResult can narrow the node set; nil means "all nodes".
//     Returning framework.Skip makes the framework skip *this plugin's* Filter too.
//   - Race to name in the note: the capacity check reads a snapshot, while other Pods
//     are being assumed concurrently. It is advisory, not a guarantee — Permit is what
//     actually enforces all-or-nothing.
func (p *TopoGang) PreFilter(_ context.Context, state *framework.CycleState, pod *v1.Pod) (*framework.PreFilterResult, *framework.Status) {
	groupUID, minMember, isGang, err := parsePodGroup(pod)
	if err != nil {
		return nil, framework.NewStatus(framework.UnschedulableAndUnresolvable, err.Error())
	}
	if !isGang {
		return nil, framework.NewStatus(framework.Skip)
	}
	if state == nil {
		return nil, framework.NewStatus(framework.Error, "cycle state is nil")
	}

	podGPURequest := gpuRequestForPod(pod, p.args.GPUResourceName)
	state.Write(preFilterStateKey, &PreFilterState{
		GroupUID:      groupUID,
		MinMember:     minMember,
		PodGPURequest: podGPURequest,
	})

	group, err := p.groups.Ensure(groupUID, minMember, time.Now().Add(p.args.PermitTimeout.Duration))
	if err != nil {
		return nil, framework.NewStatus(framework.UnschedulableAndUnresolvable, err.Error())
	}

	// A zero-GPU gang is valid, but this plugin has no useful whole-group capacity model
	// for it. Default PreFilter/Filter plugins still enforce CPU, memory, and other
	// resources per Pod; Permit will enforce the gang barrier.
	if podGPURequest == 0 {
		return nil, framework.NewStatus(framework.Success)
	}
	if p.handle == nil {
		return nil, framework.NewStatus(framework.Error, "framework handle is nil")
	}

	nodeInfos, err := p.handle.SnapshotSharedLister().NodeInfos().List()
	if err != nil {
		return nil, framework.NewStatus(framework.Error, "list node snapshot: "+err.Error())
	}

	fits, scanned := gangFitsGPU(nodeInfos, p.args.GPUResourceName, podGPURequest, group.ReservedMembers, minMember)
	preFilterNodesScanned.Observe(float64(scanned))
	if fits {
		return nil, framework.NewStatus(framework.Success)
	}

	return nil, framework.NewStatus(
		framework.Unschedulable,
		"insufficient cluster GPU capacity for the pod group",
	)
}

func gangFitsGPU(nodeInfos []*framework.NodeInfo, resourceName v1.ResourceName, podRequest int64, reserved, minMember int) (bool, int) {
	if reserved >= minMember {
		return true, 0
	}
	if podRequest <= 0 {
		return true, 0
	}
	for i, nodeInfo := range nodeInfos {
		if nodeInfo == nil || nodeInfo.Allocatable == nil || nodeInfo.Requested == nil {
			continue
		}
		allocatable := nodeInfo.Allocatable.ScalarResources[resourceName]
		requested := nodeInfo.Requested.ScalarResources[resourceName]
		free := allocatable - requested
		if free <= 0 {
			continue
		}
		reserved += int(free / podRequest)
		if reserved >= minMember {
			return true, i + 1
		}
	}
	return false, len(nodeInfos)
}

// gpuRequestForPod applies Kubernetes' basic Pod request shape for scalar resources:
// regular containers add together, while the largest init-container request is compared
// with that sum. Pod overhead cannot contain extended resources such as GPUs.
func gpuRequestForPod(pod *v1.Pod, resourceName v1.ResourceName) int64 {
	var appTotal int64
	for i := range pod.Spec.Containers {
		request := pod.Spec.Containers[i].Resources.Requests[resourceName]
		appTotal += request.Value()
	}

	var largestInit int64
	for i := range pod.Spec.InitContainers {
		quantity := pod.Spec.InitContainers[i].Resources.Requests[resourceName]
		request := quantity.Value()
		if request > largestInit {
			largestInit = request
		}
	}
	if largestInit > appTotal {
		return largestInit
	}
	return appTotal
}

// PreFilterExtensions returns the AddPod/RemovePod hooks used during preemption
// simulation. nil means this plugin's PreFilter result is not incrementally updatable,
// which is correct while the state is nil.
func (p *TopoGang) PreFilterExtensions() framework.PreFilterExtensions { return nil }

// Filter is called once per candidate node, per Pod.
//
// MUST HAND-WRITE (Day 5). Day 4 admits every node.
// Day 5: reject nodes lacking the requested GPU count, or in a topology domain that
// cannot be part of this group's placement. Note that a Filter returning Unschedulable
// for *every* node is what triggers PostFilter (preemption) — this plugin does not
// implement PostFilter, so the Pod simply stays pending.
func (p *TopoGang) Filter(_ context.Context, cs *framework.CycleState, pd *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	preFilterState, isGang, status := gangPreFilterState(cs, pd)
	if !status.IsSuccess() {
		return status
	}
	if !isGang {
		return framework.NewStatus(framework.Success)
	}

	if nodeInfo == nil || nodeInfo.Node() == nil || nodeInfo.Allocatable == nil || nodeInfo.Requested == nil {
		return framework.NewStatus(framework.Error, "nodeInfo or node is nil")
	}

	if preFilterState.PodGPURequest < 0 {
		return framework.NewStatus(framework.Error, "pod GPU request is negative")
	}
	if preFilterState.PodGPURequest == 0 {
		return framework.NewStatus(framework.Success)
	}

	allocatable := nodeInfo.Allocatable.ScalarResources[p.args.GPUResourceName]
	requested := nodeInfo.Requested.ScalarResources[p.args.GPUResourceName]
	free := allocatable - requested

	if free < preFilterState.PodGPURequest {
		return framework.NewStatus(
			framework.Unschedulable,
			fmt.Sprintf(
				"insufficient %s: requested %d, available %d",
				p.args.GPUResourceName,
				preFilterState.PodGPURequest,
				max(free, 0),
			),
		)
	}
	return framework.NewStatus(framework.Success)
}

// Score ranks a node that survived Filter. Higher is better.
//
// MUST HAND-WRITE (Day 5 core). Day 4 returns a constant so ranking is unchanged.
// Day 5: the more members of this Pod's group already placed in this node's
// nvlink-domain, the higher the score — that is the topology-aware locality signal.
// Scores must land in [framework.MinNodeScore, framework.MaxNodeScore] (0..100) or the
// framework errors out; either normalize here or do it in NormalizeScore.
func (p *TopoGang) Score(_ context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	preFilterState, isGang, status := gangPreFilterState(state, pod)
	if !status.IsSuccess() {
		return 0, status
	}
	if !isGang {
		return framework.MinNodeScore, framework.NewStatus(framework.Success)
	}
	if p.handle == nil {
		return 0, framework.NewStatus(framework.Error, "framework handle is nil")
	}

	nodeInfo, err := p.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.NewStatus(framework.Error, "get node snapshot: "+err.Error())
	}
	if nodeInfo == nil || nodeInfo.Node() == nil {
		return 0, framework.NewStatus(framework.Error, "nodeInfo or node is nil")
	}

	domain := nodeInfo.Node().Labels[p.args.TopologyKey]
	return int64(p.groups.PlacedInDomain(preFilterState.GroupUID, domain)), framework.NewStatus(framework.Success)
}

// ScoreExtensions returns the NormalizeScore hook. nil means raw Score values are used
// as-is, which is only safe because Day 4 returns a constant already inside the legal
// range. Day 5 either normalizes inside Score or returns p here.
func (p *TopoGang) ScoreExtensions() framework.ScoreExtensions { return p }

// NormalizeScore converts raw per-domain placement counts into the framework's 0..100
// score range. Equal raw scores remain neutral because no node has a locality advantage.
func (p *TopoGang) NormalizeScore(_ context.Context, _ *framework.CycleState, _ *v1.Pod, scores framework.NodeScoreList) *framework.Status {
	if len(scores) == 0 {
		return framework.NewStatus(framework.Success)
	}

	minScore, maxScore := scores[0].Score, scores[0].Score
	for i := 1; i < len(scores); i++ {
		minScore = min(minScore, scores[i].Score)
		maxScore = max(maxScore, scores[i].Score)
	}
	if minScore == maxScore {
		for i := range scores {
			scores[i].Score = framework.MinNodeScore
		}
		return framework.NewStatus(framework.Success)
	}

	for i := range scores {
		scores[i].Score = (scores[i].Score - minScore) *
			framework.MaxNodeScore /
			(maxScore - minScore)
	}
	return framework.NewStatus(framework.Success)
}

// Reserve marks the node's resources as taken by this Pod, before binding.
//
// MUST HAND-WRITE (Day 5). Day 4 is a no-op.
// Day 5: increment the group's Assigned count. Reserve and Unreserve must be exactly
// symmetric — the framework calls Unreserve on *any* later failure, including a Permit
// timeout, and an unbalanced counter permanently wedges the gang.
func (p *TopoGang) Reserve(_ context.Context, cs *framework.CycleState, pd *v1.Pod, node string) *framework.Status {
	preFilterState, isGang, status := gangPreFilterState(cs, pd)
	if !status.IsSuccess() {
		return status
	}
	if !isGang {
		return framework.NewStatus(framework.Success)
	}
	if pd == nil || pd.UID == "" {
		return framework.NewStatus(framework.Error, "pod UID is empty")
	}
	if p.handle == nil {
		return framework.NewStatus(framework.Error, "framework handle is nil")
	}

	nodeInfo, err := p.handle.SnapshotSharedLister().NodeInfos().Get(node)
	if err != nil {
		return framework.NewStatus(framework.Error, "get node snapshot: "+err.Error())
	}
	if nodeInfo == nil || nodeInfo.Node() == nil {
		return framework.NewStatus(framework.Error, "nodeInfo or node is nil")
	}

	domain := nodeInfo.Node().Labels[p.args.TopologyKey]
	if _, err := p.groups.Assign(preFilterState.GroupUID, pd.UID, node, domain, time.Now()); err != nil {
		return framework.NewStatus(framework.Error, "reserve pod in group: "+err.Error())
	}
	return framework.NewStatus(framework.Success)
}

// Unreserve rolls Reserve back. It must be idempotent: the framework may call it for a
// Pod that never successfully reserved, and it must never block or return an error.
func (p *TopoGang) Unreserve(_ context.Context, state *framework.CycleState, pod *v1.Pod, _ string) {
	preFilterState, isGang, status := gangPreFilterState(state, pod)
	if !status.IsSuccess() || !isGang || pod == nil || pod.UID == "" {
		return
	}
	p.groups.Release(preFilterState.GroupUID, pod.UID)
}

// Permit is the last extension point of the scheduling cycle and the gang gate.
//
// MUST HAND-WRITE (Day 5 core). Day 4 admits immediately with no wait.
// Day 5 semantics to get right, and the reason this is the interview question:
//   - Returning (Wait, timeout) does NOT block the scheduling goroutine. The Pod moves
//     to a waiting map and the scheduler goes on to the next Pod; only *binding* for
//     this Pod is deferred.
//   - When the minMember-th member arrives, that member's Permit must walk
//     h.IterateOverWaitingPods() and Allow() every sibling — nobody wakes them otherwise.
//   - On timeout the framework rejects this Pod and calls Unreserve. Rejecting only the
//     late member leaves the earlier ones reserved forever: reject the whole group.
//   - Returning Wait with a zero/negative timeout is a deadlock, not an infinite wait.
func (p *TopoGang) Permit(_ context.Context, state *framework.CycleState, pod *v1.Pod, _ string) (*framework.Status, time.Duration) {
	preFilterState, isGang, status := gangPreFilterState(state, pod)
	if !status.IsSuccess() {
		return status, 0
	}
	if !isGang {
		return framework.NewStatus(framework.Success), 0
	}
	if pod == nil || pod.UID == "" {
		return framework.NewStatus(framework.Error, "pod UID is empty"), 0
	}
	if p.handle == nil {
		return framework.NewStatus(framework.Error, "framework handle is nil"), 0
	}

	now := time.Now()
	decision, err := p.groups.DecidePermit(preFilterState.GroupUID, now)
	if err != nil {
		return framework.NewStatus(framework.Error, err.Error()), 0
	}
	if decision.Rejected {
		observePodGroupWait("rejected", decision.WaitStarted, now)
		gangRejects.WithLabelValues("permit_deadline").Inc()
		p.rejectWaitingGroup(preFilterState.GroupUID, "pod group permit deadline exceeded")
		return framework.NewStatus(framework.Unschedulable, "pod group permit deadline exceeded"), 0
	}
	if decision.Ready {
		observePodGroupWait("allowed", decision.WaitStarted, now)
		p.allowWaitingGroup(preFilterState.GroupUID)
		return framework.NewStatus(framework.Success), 0
	}

	remaining := decision.Deadline.Sub(now)
	if remaining <= 0 {
		return framework.NewStatus(framework.Unschedulable, "pod group permit deadline exceeded"), 0
	}
	if decision.StartTimer {
		time.AfterFunc(remaining, func() {
			if p.groups.RejectIfCollecting(preFilterState.GroupUID, decision.AttemptID, time.Now()) {
				observePodGroupWait("rejected", decision.WaitStarted, time.Now())
				gangRejects.WithLabelValues("permit_deadline").Inc()
				p.rejectWaitingGroup(preFilterState.GroupUID, "pod group permit deadline exceeded")
			}
		})
	}
	return framework.NewStatus(framework.Wait, "waiting for pod group members"), remaining
}

func readPreFilterState(state *framework.CycleState) (*PreFilterState, *framework.Status) {
	if state == nil {
		return nil, framework.NewStatus(framework.Error, "cycle state is nil")
	}
	data, err := state.Read(preFilterStateKey)
	if err != nil {
		return nil, framework.NewStatus(framework.Error, "read prefilter state: "+err.Error())
	}
	preFilterState, ok := data.(*PreFilterState)
	if !ok || preFilterState == nil {
		return nil, framework.NewStatus(framework.Error, "invalid prefilter state type")
	}
	return preFilterState, framework.NewStatus(framework.Success)
}

// gangPreFilterState keeps non-gang Pods neutral at Score/Reserve/Permit. In v1.30 a
// PreFilter Skip only suppresses this plugin's Filter and PreFilterExtensions; the other
// enabled extension points still run.
func gangPreFilterState(state *framework.CycleState, pod *v1.Pod) (*PreFilterState, bool, *framework.Status) {
	_, _, isGang, err := parsePodGroup(pod)
	if err != nil {
		return nil, false, framework.NewStatus(framework.UnschedulableAndUnresolvable, err.Error())
	}
	if !isGang {
		return nil, false, framework.NewStatus(framework.Success)
	}

	preFilterState, status := readPreFilterState(state)
	return preFilterState, true, status
}

func (p *TopoGang) waitingPodsForGroup(groupUID string) []framework.WaitingPod {
	var waitingPods []framework.WaitingPod
	iterated := 0
	p.handle.IterateOverWaitingPods(func(waitingPod framework.WaitingPod) {
		iterated++
		if waitingPod == nil {
			return
		}
		pod := waitingPod.GetPod()
		uid, _, ok, err := parsePodGroup(pod)
		if err == nil && ok && uid == groupUID {
			waitingPods = append(waitingPods, waitingPod)
		}
	})
	waitingPodsIterated.Observe(float64(iterated))
	return waitingPods
}

func (p *TopoGang) allowWaitingGroup(groupUID string) {
	for _, waitingPod := range p.waitingPodsForGroup(groupUID) {
		waitingPod.Allow(Name)
	}
}

func (p *TopoGang) rejectWaitingGroup(groupUID, reason string) {
	for _, waitingPod := range p.waitingPodsForGroup(groupUID) {
		waitingPod.Reject(Name, reason)
	}
}
