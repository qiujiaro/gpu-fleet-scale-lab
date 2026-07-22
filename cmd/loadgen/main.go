// Command loadgen submits Pods/PodGroups to the apiserver at a controlled rate.
//
// Day 2 goal: implement constant/poisson/burst arrival models + token-bucket rate
// limiting + 429 counting + Recorder. The core logic must be hand-written (see
// pkg/loadgen). This file only holds CLI wiring and may be vibe-coded.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

type flags struct {
	kubeconfig    string
	arrival       string // constant | poisson | burst
	qps           float64
	burst         int
	durationSec   int
	gpu           int
	schedulerName string
	out           string
	seed          int64
}

func parseFlags() flags {
	var f flags
	flag.StringVar(&f.kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "path to kubeconfig")
	flag.StringVar(&f.arrival, "arrival", "constant", "arrival model: constant|poisson|burst")
	flag.Float64Var(&f.qps, "qps", 30, "target submission QPS (token-bucket rate)")
	flag.IntVar(&f.burst, "burst", 50, "token-bucket burst")
	flag.IntVar(&f.durationSec, "duration", 60, "run duration seconds")
	flag.IntVar(&f.gpu, "gpu", 1, "nvidia.com/gpu request per pod")
	flag.StringVar(&f.schedulerName, "scheduler-name", "default-scheduler", "spec.schedulerName")
	flag.StringVar(&f.out, "out", "experiments/_raw/run.jsonl", "submit-log output path")
	flag.Int64Var(&f.seed, "seed", 42, "RNG seed for reproducibility")
	flag.Parse()
	return f
}

func main() {
	f := parseFlags()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; cancel() }()

	// TODO(Day2): build a clientset from kubeconfig (remember to set rest.Config.QPS/Burst,
	//             otherwise the client itself becomes the bottleneck).
	// TODO(Day2): call loadgen.Run(ctx, cs, spec, recorder).
	_ = ctx // will be passed to loadgen.Run on Day 2
	log.Printf("loadgen: arrival=%s qps=%.1f duration=%ds gpu=%d scheduler=%s out=%s seed=%d",
		f.arrival, f.qps, f.durationSec, f.gpu, f.schedulerName, f.out, f.seed)
	log.Println("TODO: implement pkg/loadgen.Run — see docs/notes/day2-loadgen.md")
}
