// Command profiler watches Pod events and computes latency breakdown (P50/P95/P99).
// Day 3: start the watch before loadgen submits, to avoid missing events.
// Core logic lives in pkg/profiler.
package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	kubeconfig := flag.String("kubeconfig", os.Getenv("KUBECONFIG"), "path to kubeconfig")
	submitLog := flag.String("submit-log", "", "loadgen JSONL with submit_ts to join")
	out := flag.String("out", "experiments/exp1-scale-sweep/run.csv", "output CSV")
	flag.Parse()

	// TODO(Day3): build a Pod informer, fill the four PodTimeline moments in UpdateFunc.
	// TODO(Day3): on completion, join the submit-log, call profiler.Quantiles, write CSV +
	//             censored rate.
	log.Printf("profiler: kubeconfig=%s submit-log=%s out=%s", *kubeconfig, *submitLog, *out)
	log.Println("TODO: implement informer + join — see docs/notes/day3-profiler.md")
}
