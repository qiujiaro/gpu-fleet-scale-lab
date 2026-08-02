// Command scheduler assembles a custom kube-scheduler binary with the topogang plugin.
//
// This is the *whole* binary: kube-scheduler's own `app` package owns flag parsing,
// leader election, the informer factory, the scheduling queue and both cycles. All we
// contribute is one out-of-tree plugin registered into the framework registry, exactly
// the pattern kubernetes-sigs/scheduler-plugins uses.
//
//	go run ./cmd/scheduler \
//	  --config config/scheduler/topogang-config.yaml \
//	  --kubeconfig ~/.kube/config -v=4
//
// It runs as a *second* scheduler: the default kube-scheduler keeps running, and the two
// never fight over a Pod because dispatch is by `pod.spec.schedulerName` — each scheduler
// only dequeues Pods whose schedulerName matches one of its own profiles.
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	"github.com/qiujiaro/gpu-fleet-scale-lab/pkg/scheduler/plugins/coalescedbind"
	"github.com/qiujiaro/gpu-fleet-scale-lab/pkg/scheduler/plugins/topogang"
)

func main() {
	// WithPlugin only puts the factory in the registry under this name; it does NOT
	// enable the plugin. Enabling happens in the KubeSchedulerConfiguration profile
	// (config/scheduler/topogang-config.yaml) — that split is deliberate, and it is the
	// most common Day 4 mistake: binary builds, scheduler starts, plugin never runs.
	command := app.NewSchedulerCommand(
		app.WithPlugin(topogang.Name, topogang.New),
		app.WithPlugin(coalescedbind.Name, coalescedbind.New),
	)
	var mutexProfileFraction, blockProfileRate int
	command.Flags().IntVar(
		&mutexProfileFraction,
		"topogang-mutex-profile-fraction",
		0,
		"sample 1 in N mutex contention events; 0 disables mutex profiling",
	)
	command.Flags().IntVar(
		&blockProfileRate,
		"topogang-block-profile-rate",
		0,
		"nanoseconds between sampled blocking events; 0 disables block profiling",
	)
	runScheduler := command.RunE
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if mutexProfileFraction < 0 || blockProfileRate < 0 {
			return fmt.Errorf("profiling rates must be non-negative")
		}
		runtime.SetMutexProfileFraction(mutexProfileFraction)
		runtime.SetBlockProfileRate(blockProfileRate)
		return runScheduler(cmd, args)
	}
	code := cli.Run(command)
	os.Exit(code)
}
