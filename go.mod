module github.com/YOURNAME/gpu-fleet-scale-lab

// NOTE (Day 4-5 pitfall): once you import k8s.io/kubernetes/pkg/scheduler/framework,
// you MUST add replace directives for every k8s.io/* staging repo pinned to the same
// version as the main module, otherwise it won't compile. On the day you do this, run
// `go get k8s.io/kubernetes@vX.Y.Z` and mirror the replace block from
// https://github.com/kubernetes-sigs/scheduler-plugins go.mod.
// Start on go 1.22; add k8s deps incrementally from Day 2 — do not pin them all at once.

go 1.22
