package topogang

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

type testSnapshot struct {
	nodes map[string]*framework.NodeInfo
}

func newTestSnapshot(nodes ...*v1.Node) *testSnapshot {
	snapshot := &testSnapshot{nodes: make(map[string]*framework.NodeInfo, len(nodes))}
	for _, node := range nodes {
		info := framework.NewNodeInfo()
		info.SetNode(node)
		snapshot.nodes[node.Name] = info
	}
	return snapshot
}

func (s *testSnapshot) NodeInfos() framework.NodeInfoLister { return s }
func (s *testSnapshot) StorageInfos() framework.StorageInfoLister {
	return s
}
func (s *testSnapshot) List() ([]*framework.NodeInfo, error) {
	result := make([]*framework.NodeInfo, 0, len(s.nodes))
	for _, node := range s.nodes {
		result = append(result, node)
	}
	return result, nil
}
func (s *testSnapshot) HavePodsWithAffinityList() ([]*framework.NodeInfo, error) {
	return nil, nil
}
func (s *testSnapshot) HavePodsWithRequiredAntiAffinityList() ([]*framework.NodeInfo, error) {
	return nil, nil
}
func (s *testSnapshot) Get(name string) (*framework.NodeInfo, error) {
	node, ok := s.nodes[name]
	if !ok {
		return nil, fmt.Errorf("node %q not found", name)
	}
	return node, nil
}
func (s *testSnapshot) IsPVCUsedByPods(string) bool { return false }

type testHandle struct {
	framework.Handle
	snapshot framework.SharedLister
	waiting  []framework.WaitingPod
}

func (h *testHandle) SnapshotSharedLister() framework.SharedLister { return h.snapshot }
func (h *testHandle) IterateOverWaitingPods(callback func(framework.WaitingPod)) {
	for _, pod := range h.waiting {
		callback(pod)
	}
}

type testWaitingPod struct {
	pod      *v1.Pod
	allowed  chan struct{}
	rejected chan string
}

func (p *testWaitingPod) GetPod() *v1.Pod                { return p.pod }
func (p *testWaitingPod) GetPendingPlugins() []string    { return []string{Name} }
func (p *testWaitingPod) Allow(string)                   { p.allowed <- struct{}{} }
func (p *testWaitingPod) Reject(_ string, reason string) { p.rejected <- reason }

func gangPod(name string, uid types.UID, minMember int) *v1.Pod {
	return &v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "ns",
		Name:      name,
		UID:       uid,
		Labels: map[string]string{
			LabelPodGroup:  "group",
			LabelMinMember: fmt.Sprint(minMember),
		},
	}}
}

func gpuGangPod(name string, uid types.UID, minMember int, gpu string) *v1.Pod {
	pod := gangPod(name, uid, minMember)
	pod.Spec.Containers = []v1.Container{{
		Name: "worker",
		Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
			defaultGPUResourceName: resource.MustParse(gpu),
		}},
	}}
	return pod
}

// TestNew constructs the Day 5 plugin without touching cluster state. Extension points
// are tested with real CycleState and NodeInfo fixtures below.
func TestNew(t *testing.T) {
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
	if tg.args.PermitTimeout.Duration != defaultPermitTimeout {
		t.Errorf("default permit timeout = %v, want %v", tg.args.PermitTimeout.Duration, defaultPermitTimeout)
	}
	if tg.args.TopologyKey != defaultTopologyKey {
		t.Errorf("default topology key = %q, want %q", tg.args.TopologyKey, defaultTopologyKey)
	}
	if tg.args.GPUResourceName != defaultGPUResourceName {
		t.Errorf("default GPU resource = %q, want %q", tg.args.GPUResourceName, defaultGPUResourceName)
	}
	normalPod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "normal"}}
	state := framework.NewCycleState()
	if _, st := tg.PreFilter(context.Background(), state, normalPod); !st.IsSkip() {
		t.Errorf("non-gang PreFilter returned %v, want Skip", st)
	}
	if score, st := tg.Score(context.Background(), state, normalPod, "unused"); !st.IsSuccess() || score != framework.MinNodeScore {
		t.Errorf("non-gang Score returned (%d, %v), want neutral success", score, st)
	}
	if st := tg.Reserve(context.Background(), state, normalPod, "unused"); !st.IsSuccess() {
		t.Errorf("non-gang Reserve returned %v", st)
	}
	if st, wait := tg.Permit(context.Background(), state, normalPod, "unused"); !st.IsSuccess() || wait != 0 {
		t.Errorf("non-gang Permit returned (%v, %v), want success without wait", st, wait)
	}
}

func TestNew_DecodesArgs(t *testing.T) {
	obj := &runtime.Unknown{Raw: []byte(`{
		"permitTimeout": "45s",
		"topologyKey": "example.com/fabric",
		"gpuResourceName": "example.com/accelerator"
	}`)}

	p, err := New(context.Background(), obj, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	args := p.(*TopoGang).args
	if args.PermitTimeout.Duration != 45*time.Second {
		t.Errorf("permit timeout = %v, want 45s", args.PermitTimeout.Duration)
	}
	if args.TopologyKey != "example.com/fabric" {
		t.Errorf("topology key = %q", args.TopologyKey)
	}
	if args.GPUResourceName != "example.com/accelerator" {
		t.Errorf("GPU resource = %q", args.GPUResourceName)
	}
}

func TestNew_RejectsInvalidArgs(t *testing.T) {
	obj := &runtime.Unknown{Raw: []byte(`{"permitTimeout":"0s"}`)}
	if _, err := New(context.Background(), obj, nil); err == nil {
		t.Fatal("New accepted a non-positive permit timeout")
	}
}

// TODO(Day5): a Pod with no gang labels yields ok=false, and such a Pod must never be
// held in Permit. This is the "one non-gang Pod deadlocks the cluster" test.
func TestPodGroupOf_NoLabels(t *testing.T) {
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod"}}
	if uid, minMember, ok := PodGroupOf(pod); ok || uid != "" || minMember != 0 {
		t.Fatalf("PodGroupOf = (%q, %d, %v), want non-gang", uid, minMember, ok)
	}
}

// TODO(Day5): minMember must parse from the label and reject <1 and non-numeric values.
// minMember=0 means Permit's "gathered" check is true before any member arrives.
func TestPodGroupOf_MinMember(t *testing.T) {
	valid := &v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "ns",
		Labels: map[string]string{
			LabelPodGroup:  "group",
			LabelMinMember: "4",
		},
	}}
	if uid, minMember, ok, err := parsePodGroup(valid); err != nil || !ok || uid != "ns/group" || minMember != 4 {
		t.Fatalf("parsePodGroup = (%q, %d, %v, %v)", uid, minMember, ok, err)
	}

	for _, value := range []string{"0", "-1", "nope"} {
		invalid := valid.DeepCopy()
		invalid.Labels[LabelMinMember] = value
		if _, _, _, err := parsePodGroup(invalid); err == nil {
			t.Errorf("parsePodGroup accepted minMember %q", value)
		}
	}
}

func TestGangFitsGPU_AccountsForNodeShapeAndAssumedRequests(t *testing.T) {
	node := func(allocatable, requested int64) *framework.NodeInfo {
		info := framework.NewNodeInfo()
		info.Allocatable.SetScalar(defaultGPUResourceName, allocatable)
		info.Requested.SetScalar(defaultGPUResourceName, requested)
		return info
	}

	tests := []struct {
		name       string
		nodes      []*framework.NodeInfo
		podRequest int64
		reserved   int
		minMember  int
		want       bool
	}{
		{
			name:       "enough members after requested resources",
			nodes:      []*framework.NodeInfo{node(8, 2)},
			podRequest: 2,
			minMember:  3,
			want:       true,
		},
		{
			name:       "aggregate GPU is fragmented below per-pod request",
			nodes:      []*framework.NodeInfo{node(1, 0), node(1, 0)},
			podRequest: 2,
			minMember:  1,
			want:       false,
		},
		{
			name:       "existing reservation completes quorum",
			nodes:      []*framework.NodeInfo{node(2, 0)},
			podRequest: 2,
			reserved:   1,
			minMember:  2,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := gangFitsGPU(tt.nodes, defaultGPUResourceName, tt.podRequest, tt.reserved, tt.minMember)
			if got != tt.want {
				t.Fatalf("gangFitsGPU = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPreFilter_WholeGroupAdmission(t *testing.T) {
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	snapshot := newTestSnapshot(node)
	nodeInfo, err := snapshot.Get(node.Name)
	if err != nil {
		t.Fatal(err)
	}
	nodeInfo.Allocatable.SetScalar(defaultGPUResourceName, 3)
	plugin := &TopoGang{
		handle: &testHandle{snapshot: snapshot},
		args:   defaultTopoGangArgs(),
		groups: NewRegistry(),
	}

	if _, status := plugin.PreFilter(
		context.Background(), framework.NewCycleState(), gpuGangPod("pod-1", "pod-1", 4, "1"),
	); status.Code() != framework.Unschedulable {
		t.Fatalf("3 GPUs for a four-member gang returned %v, want Unschedulable", status)
	}

	nodeInfo.Allocatable.SetScalar(defaultGPUResourceName, 4)
	plugin.groups = NewRegistry()
	state := framework.NewCycleState()
	if _, status := plugin.PreFilter(
		context.Background(), state, gpuGangPod("pod-1", "pod-1", 4, "1"),
	); !status.IsSuccess() {
		t.Fatalf("4 GPUs for a four-member gang returned %v, want Success", status)
	}
	if saved, status := readPreFilterState(state); !status.IsSuccess() || saved.PodGPURequest != 1 {
		t.Fatalf("saved prefilter state = %+v, status=%v", saved, status)
	}
}

func TestFilter_GPUCapacity(t *testing.T) {
	plugin := &TopoGang{args: defaultTopoGangArgs(), groups: NewRegistry()}
	state := framework.NewCycleState()
	state.Write(preFilterStateKey, &PreFilterState{
		GroupUID:      "ns/group",
		MinMember:     2,
		PodGPURequest: 2,
	})

	nodeInfo := framework.NewNodeInfo()
	nodeInfo.SetNode(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
	nodeInfo.Allocatable.SetScalar(defaultGPUResourceName, 8)
	nodeInfo.Requested.SetScalar(defaultGPUResourceName, 6)
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "ns",
		Name:      "pod-1",
		Labels: map[string]string{
			LabelPodGroup:  "group",
			LabelMinMember: "2",
		},
	}}
	if status := plugin.Filter(context.Background(), state, pod, nodeInfo); !status.IsSuccess() {
		t.Fatalf("Filter with two free GPUs returned %v", status)
	}

	nodeInfo.Requested.SetScalar(defaultGPUResourceName, 7)
	if status := plugin.Filter(context.Background(), state, pod, nodeInfo); status.Code() != framework.Unschedulable {
		t.Fatalf("Filter with one free GPU returned %v, want Unschedulable", status)
	}
}

func TestFilter_MainBranches(t *testing.T) {
	plugin := &TopoGang{args: defaultTopoGangArgs(), groups: NewRegistry()}
	pod := gangPod("pod-1", "pod-1", 2)
	validNode := framework.NewNodeInfo()
	validNode.SetNode(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})

	tests := []struct {
		name  string
		state *framework.CycleState
		node  *framework.NodeInfo
		want  framework.Code
	}{
		{"missing prefilter state", framework.NewCycleState(), framework.NewNodeInfo(), framework.Error},
		{"nil node", stateWithGPURequest(t, 1), nil, framework.Error},
		{"zero GPU request", stateWithGPURequest(t, 0), validNode, framework.Success},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plugin.Filter(context.Background(), tt.state, pod, tt.node); got.Code() != tt.want {
				t.Fatalf("Filter code = %v, want %v: %v", got.Code(), tt.want, got)
			}
		})
	}
}

func stateWithGPURequest(t *testing.T, request int64) *framework.CycleState {
	t.Helper()
	state := framework.NewCycleState()
	state.Write(preFilterStateKey, &PreFilterState{
		GroupUID: "ns/group", MinMember: 2, PodGPURequest: request,
	})
	return state
}

// TODO(Day5): two goroutines calling Get for the same UID concurrently must receive the
// *same* *PodGroupInfo. Run with -race; a check-then-create split across two lock
// acquisitions fails here.
func TestRegistry_GetIsSingleton(t *testing.T) {
	registry := NewRegistry()
	const workers = 16
	groups := make(chan *PodGroupInfo, workers)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			groups <- registry.Get("ns/group", 4, time.Now().Add(time.Minute))
		}()
	}
	wg.Wait()
	close(groups)

	var first *PodGroupInfo
	for group := range groups {
		if first == nil {
			first = group
			continue
		}
		if group != first {
			t.Fatal("concurrent Get calls returned different group instances")
		}
	}
}

// TODO(Day5): Assign then Release returns to zero; a second Release must not go negative
// (Unreserve is called for Pods that never reserved).
func TestRegistry_ReleaseIsIdempotent(t *testing.T) {
	registry := NewRegistry()
	registry.Get("ns/group", 2, time.Now().Add(time.Minute))
	podUID := types.UID("pod-1")

	if assigned, err := registry.Assign("ns/group", podUID, "node-1", "domain-a", time.Now()); err != nil {
		t.Fatalf("Assign: %v", err)
	} else if assigned != 1 {
		t.Fatalf("assigned = %d, want 1", assigned)
	}
	if assigned, err := registry.Assign("ns/group", podUID, "node-1", "domain-a", time.Now()); err != nil {
		t.Fatalf("duplicate Assign: %v", err)
	} else if assigned != 1 {
		t.Fatalf("assigned after duplicate = %d, want 1", assigned)
	}

	registry.Release("ns/group", podUID)
	registry.Release("ns/group", podUID)

	if got := registry.PlacedInDomain("ns/group", "domain-a"); got != 0 {
		t.Fatalf("placed after repeated Release = %d, want 0", got)
	}
}

// Score normalization must land in the framework's legal range.
func TestScore_Normalized(t *testing.T) {
	plugin := &TopoGang{}
	scores := framework.NodeScoreList{
		{Name: "node-a", Score: 1},
		{Name: "node-b", Score: 3},
		{Name: "node-c", Score: 5},
	}
	if status := plugin.NormalizeScore(context.Background(), nil, nil, scores); !status.IsSuccess() {
		t.Fatalf("NormalizeScore returned %v", status)
	}
	if scores[0].Score != framework.MinNodeScore ||
		scores[1].Score != framework.MaxNodeScore/2 ||
		scores[2].Score != framework.MaxNodeScore {
		t.Fatalf("normalized scores = %#v", scores)
	}
}

func TestScore_PrefersPopulatedTopologyDomain(t *testing.T) {
	nodes := []*v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{defaultTopologyKey: "domain-a"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node-b", Labels: map[string]string{defaultTopologyKey: "domain-b"}}},
	}
	handle := &testHandle{snapshot: newTestSnapshot(nodes...)}
	plugin := &TopoGang{handle: handle, args: defaultTopoGangArgs(), groups: NewRegistry()}
	if _, err := plugin.groups.Ensure("ns/group", 2, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.groups.Assign("ns/group", "placed", "node-a", "domain-a", time.Now()); err != nil {
		t.Fatal(err)
	}

	pod := gangPod("pod-2", "pod-2", 2)
	state := stateWithGPURequest(t, 1)
	a, status := plugin.Score(context.Background(), state, pod, "node-a")
	if !status.IsSuccess() {
		t.Fatal(status)
	}
	b, status := plugin.Score(context.Background(), state, pod, "node-b")
	if !status.IsSuccess() {
		t.Fatal(status)
	}
	if a <= b {
		t.Fatalf("scores domain-a=%d domain-b=%d, want populated domain higher", a, b)
	}
}

// The first member waits and owns the timer; the minMember-th member makes the group ready.
func TestPermit_WaitsUntilMinMember(t *testing.T) {
	registry := NewRegistry()
	deadline := time.Now().Add(time.Minute)
	group, err := registry.Ensure("ns/group", 2, deadline)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := registry.Assign("ns/group", "pod-1", "node-1", "domain-a", time.Now()); err != nil {
		t.Fatalf("Assign first member: %v", err)
	}

	first, err := registry.DecidePermit("ns/group", time.Now())
	if err != nil {
		t.Fatalf("DecidePermit first member: %v", err)
	}
	if first.Ready || first.Rejected || !first.StartTimer || first.AttemptID != group.AttemptID {
		t.Fatalf("first decision = %+v, want one waiting timer owner", first)
	}

	if _, err := registry.Assign("ns/group", "pod-2", "node-2", "domain-a", time.Now()); err != nil {
		t.Fatalf("Assign second member: %v", err)
	}
	second, err := registry.DecidePermit("ns/group", time.Now())
	if err != nil {
		t.Fatalf("DecidePermit second member: %v", err)
	}
	if !second.Ready || second.Rejected {
		t.Fatalf("second decision = %+v, want Ready", second)
	}
}

// An expired group becomes rejected exactly once.
func TestPermit_TimeoutRejectsWholeGroup(t *testing.T) {
	registry := NewRegistry()
	deadline := time.Now().Add(-time.Second)
	group, err := registry.Ensure("ns/group", 2, deadline)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := registry.Assign("ns/group", "pod-1", "node-1", "domain-a", time.Now()); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	decision, err := registry.DecidePermit("ns/group", time.Now())
	if err != nil {
		t.Fatalf("DecidePermit: %v", err)
	}
	if !decision.Rejected {
		t.Fatalf("decision = %+v, want Rejected", decision)
	}
	if registry.RejectIfCollecting("ns/group", group.AttemptID, time.Now()) {
		t.Fatal("already rejected group was rejected a second time")
	}
}

func TestPermit_TimeoutReleasesAndAllowsFreshAttempt(t *testing.T) {
	timeout := 40 * time.Millisecond
	waiting := &testWaitingPod{
		pod:     gangPod("pod-1", "pod-1", 2),
		allowed: make(chan struct{}, 1), rejected: make(chan string, 1),
	}
	handle := &testHandle{waiting: []framework.WaitingPod{waiting}}
	plugin := &TopoGang{
		handle: handle,
		args: TopoGangArgs{
			PermitTimeout: metav1.Duration{Duration: timeout},
			TopologyKey:   defaultTopologyKey, GPUResourceName: defaultGPUResourceName,
		},
		groups: NewRegistry(),
	}
	state := stateWithGPURequest(t, 1)
	first, err := plugin.groups.Ensure("ns/group", 2, time.Now().Add(timeout))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.groups.Assign("ns/group", "pod-1", "node-a", "domain-a", time.Now()); err != nil {
		t.Fatal(err)
	}

	status, wait := plugin.Permit(context.Background(), state, waiting.pod, "node-a")
	if status.Code() != framework.Wait || wait <= 0 {
		t.Fatalf("Permit = (%v, %v), want positive Wait", status, wait)
	}
	select {
	case reason := <-waiting.rejected:
		if reason == "" {
			t.Fatal("whole-group rejection had no reason")
		}
	case <-time.After(time.Second):
		t.Fatal("Permit timeout did not reject the waiting group (possible deadlock)")
	}

	// The scheduler calls Unreserve after a Permit rejection. Releasing the final
	// reservation deletes the rejected attempt, so a requeued Pod can create a clean one.
	plugin.Unreserve(context.Background(), state, waiting.pod, "node-a")
	second, err := plugin.groups.Ensure("ns/group", 2, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.AttemptID == first.AttemptID || second.State != Pending || second.ReservedMembers != 0 {
		t.Fatalf("fresh attempt = %+v, previous attempt=%d", second, first.AttemptID)
	}
}
