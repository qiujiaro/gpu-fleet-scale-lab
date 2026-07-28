package topogang

import (
	"context"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

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
			got := gangFitsGPU(tt.nodes, defaultGPUResourceName, tt.podRequest, tt.reserved, tt.minMember)
			if got != tt.want {
				t.Fatalf("gangFitsGPU = %v, want %v", got, tt.want)
			}
		})
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
