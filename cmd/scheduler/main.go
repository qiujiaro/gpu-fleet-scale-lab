// Command scheduler assembles a custom kube-scheduler binary with the topogang plugin.
//
// Day 4-5: mirror the assembly pattern from kubernetes-sigs/scheduler-plugins.
// Run it as a second scheduler (schedulerName=topogang), compared against the default one.
//
// Uncommenting requires first importing k8s.io/kubernetes into go.mod and aligning every
// staging replace directive (see the note in go.mod).
package main

import "log"

func main() {
	// import (
	//   "os"
	//   "k8s.io/component-base/cli"
	//   "k8s.io/kubernetes/cmd/kube-scheduler/app"
	//   "github.com/YOURNAME/gpu-fleet-scale-lab/pkg/scheduler/plugins/topogang"
	// )
	// command := app.NewSchedulerCommand(
	//   app.WithPlugin(topogang.Name, topogang.New),
	// )
	// code := cli.Run(command)
	// os.Exit(code)
	log.Println("TODO(Day4): wire app.NewSchedulerCommand(WithPlugin(topogang.Name, topogang.New))")
}
