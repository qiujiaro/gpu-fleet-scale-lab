# Day 4 — Second scheduler: scaffold, TODO list, and reference URLs

Scaffold is in place and `go build ./... && go vet ./... && go test ./...` is green.
Everything marked **HAND-WRITE** below is a stub that compiles and returns a *neutral*
value — that is deliberate. The tests for those stubs are written but `t.Skip`ped; unskip
each one as you implement it. `TestNew_NeutralSkeleton` is **not** skipped: it is the
Day 4 acceptance test in unit form and must stay green today.

The dependency pin is done (guide §7, " vibe coding") — but review it, because it is
the thing most likely to break when you bump versions:

- `go.mod` now requires `k8s.io/kubernetes v1.30.0` plus a `replace` block pinning **every**
  `k8s.io/*` staging repo to `v0.30.0`. The main repo publishes staging modules as `v0.0.0`
  placeholders, so without the replaces you get `unknown revision v0.0.0`.
- The version pair must stay consistent: `k8s.io/kubernetes vA.B.C` ⇒ every staging repo at
  `v0.B.C`. Bumping one without the others is the classic silent breakage.
- Mirrored from [scheduler-plugins `go.mod`](https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/go.mod).

## What the scaffold gives you

| File | Status | Why |
|---|---|---|
| `go.mod` replace block | done (vibe) | staging pin — review the version consistency yourself |
| `cmd/scheduler/main.go` | done (vibe) | `app.NewSchedulerCommand(WithPlugin(...))` + `cli.Run` is the official 8-line pattern |
| `pkg/scheduler/plugins/topogang/plugin.go` | **HAND-WRITE** `New` + the extension-point bodies | the framework contract is the lesson; the stubs give you the exact v1.30 signatures |
| `pkg/scheduler/plugins/topogang/podgroup.go` | **HAND-WRITE** `Registry` methods (Day 5) | gang state + locking; `PodGroupOf` label parsing is boilerplate |
| `config/scheduler/topogang-config.yaml` | **HAND-WRITE** the `profiles[]` entry | this is where a registered plugin becomes a running plugin |
| `pkg/scheduler/plugins/topogang/plugin_test.go` | 1 live test + skipped stubs | each skipped test names one rule you must implement |
| `scripts/day4-two-schedulers.sh` | done (vibe) | the §8 split-workload experiment + the kubectl assertions |

Design choice carried over from Day 3: the gang state lives in `Registry` (plain Go, no
client-go), so the Day 5 state machine is unit-testable with no cluster — same role
`Tracker` plays in `pkg/profiler` and `Submitter` in `pkg/loadgen`.

Three names that are **not** the same name, and confusing them is the whole Day 4 bug class:

| Name | Value | Where |
|---|---|---|
| plugin registry key | `TopoGang` (`topogang.Name`) | `WithPlugin(...)`, `plugins.*.enabled[].name`, `pluginConfig[].name` |
| profile / scheduler name | `topogang` | `profiles[].schedulerName`, Pod `spec.schedulerName`, `loadgen --scheduler-name` |
| Go package | `topogang` | import path only |

## TODO list

### A. Binary assembly — `cmd/scheduler/main.go` (written; verify you can explain it)
- [ ] A1. `app.WithPlugin(Name, New)` only puts the factory in the **registry**. It does not enable the plugin. Enabling is the profile's job — this split is the #1 "builds fine, never runs" trap.
- [ ] A2. `cli.Run(command)` (not `command.Execute()`) is what wires klog flags, `--v`, and the exit code. Know why you want `-v=4` today.
- [ ] A3. You are inheriting the *entire* real scheduler: queue, snapshot, both cycles, leader election, metrics on `:10259`. Be able to say what you did and did not write.
- Refs: [Scheduling Framework concepts](https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/) · [scheduler-plugins README — build a scheduler with plugins](https://github.com/kubernetes-sigs/scheduler-plugins) · [`app.WithPlugin`](https://pkg.go.dev/k8s.io/kubernetes/cmd/kube-scheduler/app#WithPlugin)

### B. Plugin skeleton — `New` + extension points (must hand-write)
- [ ] B1. `New(ctx, obj runtime.Object, h framework.Handle)` — `obj` is this plugin's decoded `pluginConfig[].args`, `nil` when the profile provides none. Ignore it today; fail loudly on malformed args rather than defaulting silently.
- [ ] B2. Keep the `framework.Handle`. It is the only door back to cluster state (`SnapshotSharedLister`, `ClientSet`, `IterateOverWaitingPods`) and you cannot recover it later.
- [ ] B3. Implement the interface *signatures* exactly for the pinned version. The `var _ framework.XPlugin = &TopoGang{}` assertions in `plugin.go` exist because a signature typo otherwise produces a plugin that registers fine and is never called — the failure mode is silence, not a compile error.
- [ ] B4. Neutral really means neutral: `Filter` admits every node, `Score` returns the **same** value for every node, `Permit` returns `Success, 0`. If Day 4 changes placement at all, Day 5's comparison against `default-scheduler` is measuring your bug.
- [ ] B5. One plugin instance is shared across scheduling goroutines. Anything mutable on the struct needs a lock — `Registry` has one; do not add unguarded fields.
- [ ] B6. Know which side of the cycle boundary each point is on: **scheduling cycle** = QueueSort → PreEnqueue → PreFilter → Filter → PostFilter → PreScore → Score → NormalizeScore → Reserve → Permit (serial, one Pod at a time). **binding cycle** = WaitOnPermit → PreBind → Bind → PostBind (runs concurrently in a goroutine). Reserve is the boundary: after it, resources are *assumed* in the cache even though etcd knows nothing yet.
- Refs: [`framework` interfaces (pkg.go.dev)](https://pkg.go.dev/k8s.io/kubernetes/pkg/scheduler/framework) · [`PluginFactory`](https://pkg.go.dev/k8s.io/kubernetes/pkg/scheduler/framework/runtime#PluginFactory) · [scheduler-plugins `pkg/coscheduling`](https://github.com/kubernetes-sigs/scheduler-plugins/tree/master/pkg/coscheduling)

### C. KubeSchedulerConfiguration wiring — `topogang-config.yaml` (must hand-write)
- [ ] C1. `profiles[].schedulerName: topogang` must byte-match what loadgen passes to `--scheduler-name`. A typo does not error: the Pods just sit `Pending` with **no events**, because no scheduler in the cluster claims them. Experience this once deliberately — it is reading question #3 made physical.
- [ ] C2. Enable `TopoGang` under exactly the extension points the plugin implements. Enabling one it does not implement is a startup error; implementing one and forgetting to enable it is silent.
- [ ] C3. Do **not** disable the default plugins. Out-of-tree enabling is additive; `NodeResourcesFit` / `NodeUnschedulable` / `TaintToleration` must keep running or "neutral" is a lie.
- [ ] C4. `leaderElect: false` for a single local process. Two schedulers contending for one lease means one sits idle and you debug the wrong thing.
- [ ] C5. Raise `clientConnection.qps/burst` above the 50/100 default — Day 3 already showed client-side throttling at N≥500, and the scheduler is a client too.
- Refs: [kube-scheduler-config v1 reference](https://kubernetes.io/docs/reference/config-api/kube-scheduler-config.v1/) · [Configure Multiple Schedulers](https://kubernetes.io/docs/tasks/extend-kubernetes/configure-multiple-schedulers/)

### D. Gang state skeleton — `podgroup.go` (Day 5 core, shape it today)
- [ ] D1. Group identity comes off the Pod (`topogang.dev/pod-group` + `topogang.dev/min-member`) and must be namespace-qualified — group names are only unique within a namespace.
- [ ] D2. A Pod with **no** gang labels must never be held in `Permit`. One non-gang Pod stuck waiting is a cluster-wide stall.
- [ ] D3. `Registry.Get` is check-then-create under a **single** held lock; two concurrent members must get the same `*PodGroupInfo`. Test it with `-race`.
- [ ] D4. One lock on the `Registry`, not a second one per group. Registry-lock-then-group-lock, with `Permit` callbacks running in between, is a lock-ordering deadlock waiting to happen.
- [ ] D5. `Reserve`/`Unreserve` must be exactly symmetric and `Release` idempotent with a floor at zero — the framework calls `Unreserve` for Pods that never reserved, and an unbalanced counter wedges the gang permanently.
- Refs: [PodGroup coscheduling KEP](https://github.com/kubernetes-sigs/scheduler-plugins/tree/master/kep/42-podgroup-coscheduling) · [NVIDIA Grove (motivation)](https://github.com/ai-dynamo/grove)

### E. Two-scheduler run — the §8 experiment
- [ ] E1. Terminal A: `go run ./cmd/scheduler --config config/scheduler/topogang-config.yaml --kubeconfig ~/.kube/config -v=4`. Confirm in the log that the `topogang` profile registered **and** that `TopoGang` appears in its enabled plugin list.
- [ ] E2. `scripts/day4-two-schedulers.sh` — same workload, half `default-scheduler`, half `topogang`, submitted concurrently.
- [ ] E3. Acceptance: both halves reach `bound` (non-empty `spec.nodeName`) and `Running`; zero Pods stuck `Pending`; the default half is unaffected.
- [ ] E4. Confirm non-interference from the *other* side too: the `Scheduled` events for `topogang` Pods must name your scheduler as the source, and your scheduler's log must never mention a `default-scheduler` Pod.
- [ ] E5. Optional but cheap: run `cmd/profiler` over the `topogang` half and record its scheduling P99 next to Day 3's default-scheduler baseline. Today's number is the *skeleton overhead* — with a neutral plugin it should be indistinguishable from default. If it is not, C3 is wrong.
- [ ] E6. Fill `docs/notes/logs/Day04.md` and `docs/notes/day4-framework.md` (the extension-point order diagram), and commit the config + plugin skeleton.
- Refs: [Configure Multiple Schedulers — verify the pods were scheduled](https://kubernetes.io/docs/tasks/extend-kubernetes/configure-multiple-schedulers/#verifying-that-the-pods-were-scheduled-using-the-desired-schedulers) · [kube-scheduler-simulator (optional debugger)](https://kubernetes.io/blog/2025/04/07/introducing-kube-scheduler-simulator/)

## Per-function briefs

What each stub has to do, and the types you will be holding while you do it. No code —
the signatures are already in the files, and the point of the exercise is deciding the
semantics. Type names below are verified against the pinned `v1.30.0`; they drift across
releases, so re-check on pkg.go.dev before trusting any of this against a bumped version.

Types that show up everywhere, once:

- **`*framework.Status`** — the universal return. Its `Code` is one of `Success`, `Error`,
  `Unschedulable`, `UnschedulableAndUnresolvable`, `Wait`, `Skip`, `Pending`. A **nil**
  Status counts as Success. The distinction that matters: `Error` means *your plugin is
  broken* (the Pod is requeued without backoff and the scheduler logs an internal error);
  `Unschedulable` means *the cluster cannot host this Pod right now* (normal, backoff
  applies, PostFilter/preemption may run). Never use `Error` for "no room".
  `UnschedulableAndUnresolvable` additionally says "preemption will not help" — that is
  the right code for a gang that can never fit, since evicting victims for one member
  does not make the group fit.
- **`*framework.CycleState`** — a `sync.Map` scoped to *one Pod, one scheduling cycle*,
  keyed by `framework.StateKey` (a string type) holding `framework.StateData` (an
  interface whose only method is `Clone() StateData`). This is how PreFilter hands work
  to Filter/Score/Reserve without recomputing. It is discarded at the end of the cycle
  and it is **not** where gang state lives.
- **`framework.Handle`** — the plugin's only door outward. The methods you will care
  about: `SnapshotSharedLister()`, `IterateOverWaitingPods(func(WaitingPod))`,
  `GetWaitingPod(types.UID)`, `RejectWaitingPod(types.UID)`, `ClientSet()`,
  `SharedInformerFactory()`, `EventRecorder()`, `Parallelizer()`.

### `New` — `plugin.go` (Day 4)

**What to do.** Decide what a malformed config should do, then build the instance. `obj`
is the decoded `pluginConfig[].args` for *this* plugin and is `nil` when the profile
provides none — so you need a "no args" path today and an args-typed path on Day 5.
Anything you want at extension-point time must be captured now: the handle, the registry,
any parsed args. Returning an error aborts scheduler startup, which is the correct
response to a config you cannot honour — a plugin that silently defaults is worse than
one that refuses to boot. Do not touch the cluster here: informers have not started and
the snapshot is empty, so any read is either nil or a lie.

**Data structures.** `runtime.Object` (`k8s.io/apimachinery/pkg/runtime`) — the raw args,
which on Day 5 you type-assert to your own args struct registered with the scheduler's
scheme. `framework.Handle`. `framework.Plugin` — the return interface, whose only method
is `Name() string`; everything else is discovered by the framework type-asserting your
value against `PreFilterPlugin`, `FilterPlugin`, and so on. That type-assertion is why the
`var _ framework.XPlugin = &TopoGang{}` lines exist.

### `PreFilter` — `plugin.go` (Day 5)

**What to do.** Three jobs, in order. (1) Identify the gang: pull the group UID and
minMember off the Pod once, here, rather than in Filter where you would redo it per node.
(2) Stash what downstream points need into `CycleState` under your own key — Filter and
Score run hot, and re-parsing labels per node is the standard performance mistake. (3)
Decide whether the *whole group* can fit, and reject early if it cannot. That third job is
the entire reason gang scheduling has a PreFilter: without it, members trickle in one at a
time, each individually schedulable, and you end up with a half-placed group holding GPUs
that nothing can use.

Decisions to make explicitly: what "the whole group fits" means when you only have a
snapshot (sum of free GPUs across nodes? free GPUs in a single domain? largest domain?) —
and be honest in the note that whichever you pick is *advisory*, because other Pods are
being assumed concurrently and the snapshot is already stale. Permit is the real
enforcement. Also decide what a Pod with no gang labels does here: it must sail straight
through, and ideally return `Skip` so the framework does not even call your Filter for it.

**Data structures.** `*v1.Pod` (labels/annotations, `Spec.Containers[].Resources.Requests`
for the `nvidia.com/gpu` count as a `resource.Quantity`). `*framework.PreFilterResult`,
whose single field `NodeNames sets.Set[string]` narrows the candidate set — `nil` means
"all nodes", which is what you want unless you are deliberately pruning. Your own
`StateData` implementation for the stash. For the capacity read: `Handle.SnapshotSharedLister()`
→ `framework.SharedLister` → `NodeInfos()` → `NodeInfoLister.List() ([]*NodeInfo, error)`.

### `PreFilterExtensions` — `plugin.go`

**What to do.** Return nil unless you can *incrementally* update the PreFilter stash when
a hypothetical victim Pod is added to or removed from a node during preemption
simulation. Since this plugin has no PostFilter, nothing simulates preemption against it,
and nil is correct and honest. Returning a non-nil implementation you have not thought
through means preemption silently reasons against stale group state.

**Data structures.** `framework.PreFilterExtensions` — `AddPod`/`RemovePod`, each taking
the `CycleState`, the Pod being scheduled, a `*framework.PodInfo` for the
added/removed Pod, and the `*framework.NodeInfo`.

### `Filter` — `plugin.go` (Day 5)

**What to do.** Answer one boolean per node: can *this* Pod run *here*, ignoring how good
a fit it is. Quality is Score's job, and conflating the two is the classic mistake — a
Filter that rejects "suboptimal" nodes turns a soft preference into a hard constraint and
produces unschedulable Pods under load. Read your stash from CycleState rather than
re-deriving it. Two things worth rejecting on: insufficient free GPUs on the node, and a
topology domain incompatible with where the group is already landing (if you decide to
enforce domain affinity hard rather than via Score — decide which, and write down why).

Note the accounting subtlety: `NodeInfo.Requested` includes *assumed* Pods — ones the
scheduler has sent to bind but which etcd may not have accepted yet. That is what you
want; using the live API server's view instead would double-book.

If you return Unschedulable for every node, the framework runs PostFilter (preemption).
You have no PostFilter, so the Pod simply stays Pending and is requeued with backoff.

**Data structures.** `*framework.NodeInfo`: `Node() *v1.Node` for labels (the topology key
here is `topology.nvidia.com/nvlink-domain`, per `config/kwok/node-template.yaml`),
`Allocatable *framework.Resource` and `Requested *framework.Resource` for capacity, and
`Pods []*framework.PodInfo` if you need to inspect co-tenants. `framework.Resource` keeps
CPU/memory/ephemeral-storage as typed int64 fields and everything else — including
`nvidia.com/gpu` — in `ScalarResources map[v1.ResourceName]int64`, so GPUs are a map
lookup, not a struct field.

### `Score` — `plugin.go` (Day 5)

**What to do.** Turn "how much do I like this node for this Pod" into an integer. The
locality signal: the more members of this Pod's group are already placed in this node's
nvlink-domain, the higher the score — that is what makes the gang land together instead of
smeared across the fleet. Two things to get right. First, the range: the framework
requires `[framework.MinNodeScore, framework.MaxNodeScore]` (0..100) and errors out
otherwise, so normalize against something — group size, or the max observed count — and
handle the degenerate cases (zero placed, placed exceeding your denominator, a group of
one). Second, the weight: your score is summed with the default plugins' scores after
multiplication by the profile's `weight`, so a "correct" score with weight 1 against
`NodeResourcesFit` is a rounding error. The weight is a config decision, and it belongs in
the note.

Also: Score is only called for nodes that survived Filter, and it is called concurrently
across nodes via the parallelizer — so reads of the shared registry must be under its
lock, and must not be *writes*.

**Data structures.** `int64` return in `[MinNodeScore, MaxNodeScore]`. `framework.NodeScoreList`
(`[]framework.NodeScore{Name, Score}`) if you implement NormalizeScore. Your registry's
per-domain counter map. The node name arrives as a plain `string` — you must resolve it
to a domain yourself, via the snapshot lister or a cached label map.

### `ScoreExtensions` / `NormalizeScore` — `plugin.go`

**What to do.** Return nil if Score already emits values in range. Return the plugin if you
want to rescale *after* seeing all nodes' raw scores — which is the honest choice when
your natural metric has no fixed upper bound (e.g. raw member counts), because only then
do you know the maximum. NormalizeScore mutates the score list in place.

**Data structures.** `framework.ScoreExtensions` — one method, `NormalizeScore(ctx, state,
pod, scores NodeScoreList) *Status`, mutating `scores`.

### `Reserve` / `Unreserve` — `plugin.go` (Day 5)

**What to do.** Reserve is the boundary between "thinking" and "committed": after it, the
scheduler's cache treats those resources as taken even though nothing has been written to
etcd. Your job is to increment the group's assigned count and its per-domain tally, so the
next member's Score can see it. Unreserve is the rollback, and the framework calls it on
*any* subsequent failure in the cycle — including a Permit timeout and a failed bind.

The rules that actually bite: Unreserve must be idempotent and must never drive the count
negative, because it is called for Pods that never successfully reserved. It must not
block or return an error — it has no way to report one. And the pair must be exactly
symmetric: every path that increments must have a path that decrements, or the group's
count drifts upward and the gang never reaches minMember again — a permanent wedge that
looks like a hang, not a crash.

**Data structures.** Your `Registry`/`PodGroupInfo` counters. `nodeName string`, which you
must map to a domain the same way Score does — and it must be the *same* mapping, or
Reserve credits a domain that Score never reads.

### `Permit` — `plugin.go` (Day 5, the core)

**What to do.** This is the gang gate and the interview question. Returning
`(Wait, timeout)` does **not** block the scheduling goroutine: the framework parks this
Pod in a waiting map and moves on to the next Pod; only this Pod's *binding* is deferred,
in its own goroutine. So the shape is: count the members that have reached this point; if
fewer than minMember, return Wait with a positive timeout; if this Pod is the one that
completes the group, allow yourself *and* walk the waiting siblings and allow each of
them — nobody wakes them otherwise, and a group that has met its quorum but is never
released is the most common gang bug.

Timeout semantics are the second half. On expiry the framework rejects this Pod and calls
Unreserve for it — but only for it. Rejecting just the late member leaves the earlier
members reserved and waiting forever, so your timeout path must reject the whole group.
A zero or negative timeout with `Wait` is not "wait forever", it is an immediate deadlock.
And a Pod that is not part of any gang must never be parked here: one non-gang Pod stuck
in Permit is a cluster-wide stall.

Deadlock to reason about before you write it: releasing siblings means calling into
`WaitingPod.Allow` from inside your own Permit while holding your registry lock, and
those callbacks run on other goroutines. Decide your lock discipline first — collect the
UIDs to release under the lock, release the lock, then call `Allow` — and write down why.

**Data structures.** Return is `(*framework.Status, time.Duration)`; the Status code must
be `Wait` for the duration to mean anything. `framework.WaitingPod` — `GetPod() *v1.Pod`,
`GetPendingPlugins() []string`, `Allow(pluginName string)`, `Reject(pluginName, msg string)`.
Reached via `Handle.IterateOverWaitingPods(func(WaitingPod))` to sweep, or
`Handle.GetWaitingPod(types.UID)` / `Handle.RejectWaitingPod(types.UID)` for one known Pod.
Note `Allow`/`Reject` take *your plugin name* — the framework tracks which plugins still
hold a Pod, and it unblocks only when the last one lets go.

### `PodGroupOf` — `podgroup.go` (Day 5, boilerplate but decide the edges)

**What to do.** Map a Pod to `(groupUID, minMember, ok)`. `ok=false` for a Pod with no
gang labels is the single most important return value in the file — everything downstream
uses it to skip the gang machinery entirely. Namespace-qualify the UID: group names are
only unique within a namespace, and a bare name lets two teams' groups merge into one
gang. Validate minMember: non-numeric, absent, zero and negative all have to become
`ok=false` (or an error you can surface), because a minMember of 0 makes "quorum reached"
true before any member arrives, and the gang silently degrades to no gang at all.

**Data structures.** `*v1.Pod` → `ObjectMeta.Labels map[string]string` and
`ObjectMeta.Namespace`. Keys are the `LabelPodGroup` / `LabelMinMember` constants. The
real-world alternative, which scheduler-plugins uses, is a PodGroup CRD — worth one line
in the note on what labels cost you (no group-level status, no validation, no defaulting).

### `Registry.Get` — `podgroup.go` (Day 5)

**What to do.** Return the one shared state object for a group, creating it on first
sight. The correctness requirement is that two members arriving concurrently get the
*same* pointer — which means look-up and create happen under a single held lock, not
"check, unlock, create, lock". Splitting them produces two groups, each counting to
minMember separately, and the symptom is a gang that never releases under load and works
perfectly in single-threaded tests. This is what `-race` plus a concurrent test is for.

Creation is also where minMember and the deadline get fixed. Decide whose value wins if
two members disagree about minMember (first writer? reject the mismatch?) and whether the
deadline is set at group creation or refreshed per member — the second choice makes
timeout unbounded if members keep trickling in.

**Data structures.** `map[string]*PodGroupInfo` guarded by `sync.Mutex`. `PodGroupInfo`
holds `UID`, `MinMember`, `Assigned`, `PlacedByDomain map[string]int`, `Deadline time.Time`,
`State PodGroupState`. Note the deliberate design choice already in the file: the lock is
on the Registry, not per-group — two-level locking with Permit callbacks running in
between is a lock-ordering deadlock waiting to happen.

### `Registry.Assign` / `Release` / `PlacedInDomain` — `podgroup.go` (Day 5)

**What to do.** `Assign` records one member reserving a domain and returns the new count,
so the caller can tell whether this Pod completed the quorum without a second lock
acquisition — return the count, do not make the caller re-read. It is also the natural
place for the `Collecting → Ready` transition. `Release` is the inverse with a floor at
zero and idempotency, per the Unreserve contract above; decide whether a group dropping to
zero members is deleted from the map (leak-free, but loses the deadline if a member comes
back) or retained (simpler, but the map grows for the process lifetime — bounded by
distinct group names, which in a benchmark loop is not bounded at all).

`PlacedInDomain` is a read, but still under the lock — it is called from Score, which runs
concurrently across nodes.

**Data structures.** `PodGroupInfo.Assigned int`, `PodGroupInfo.PlacedByDomain map[string]int`,
`PodGroupState` (`Pending`/`Collecting`/`Ready`/`Bound`/`Rejected`). Worth noting in the
state machine diagram: `Rejected` is terminal for *this attempt only* — the members are
requeued and the group is rebuilt from scratch next time.

## Run

```bash
# terminal A — your scheduler, first
go run ./cmd/scheduler \
  --config config/scheduler/topogang-config.yaml \
  --kubeconfig ~/.kube/config -v=4

# terminal B — the split workload (default-scheduler + topogang, concurrently)
./scripts/day4-two-schedulers.sh

# or by hand, one half at a time
go run ./cmd/loadgen --arrival constant --qps 10 --duration 60 \
  --scheduler-name topogang --out experiments/_raw/day4-topogang.jsonl
kubectl get pods -o wide --field-selector spec.schedulerName=topogang
```

`-v=4` is what makes the plugin visible in the log; at the default `-v=2` a plugin that is
registered-but-not-enabled looks exactly like one that is working.

## Reading-question answers (fill in after the run)

1. What is the full order of the extension points, and where is the scheduling/binding cycle boundary?
2. How does a scheduler binary register a custom plugin into the framework (`app.NewSchedulerCommand(WithPlugin(...))`), and what does registration *not* do?
3. How do the second scheduler and the default scheduler avoid both scheduling the same Pod — and what did you observe when the name did not match?
