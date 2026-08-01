// Command profiler watches Pod events and computes the created→scheduled→bound→ready
// latency breakdown with P50/P95/P99.
//
// Day 3: start this BEFORE loadgen submits, so no early watch events are missed.
// The measurement logic lives in pkg/profiler and must be hand-written; this file is
// CLI + client-go wiring and may be vibe-coded (but still reviewed).
//
//	go run ./cmd/profiler --kubeconfig ~/.kube/config \
//	  --submit-log experiments/diagnostics/exp1-N500.jsonl \
//	  --out experiments/diagnostics/rejected-pod-latency-baseline/N500.csv --duration 180s
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/qiujiaro/gpu-fleet-scale-lab/pkg/profiler"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type flags struct {
	kubeconfig      string
	namespace       string
	labelSelector   string
	submitLog       string
	out             string
	summaryOut      string
	groupOut        string
	groupSummaryOut string
	duration        time.Duration
	drain           time.Duration
	promURL         string
	qps             float64
	burst           int
}

func parseFlags() flags {
	var f flags
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	flag.StringVar(&f.kubeconfig, "kubeconfig", kubeconfig, "path to kubeconfig")
	flag.StringVar(&f.namespace, "namespace", "default", "namespace to watch")
	flag.StringVar(&f.labelSelector, "label-selector", "", "narrow the watch to this run's pods")
	flag.StringVar(&f.submitLog, "submit-log", "", "loadgen JSONL with submit_ts to join (required)")
	flag.StringVar(&f.out, "out", "experiments/diagnostics/rejected-pod-latency-baseline/run.csv", "per-pod timeline CSV")
	flag.StringVar(&f.summaryOut, "summary-out", "", "per-phase quantile CSV (default: <out> with -summary suffix)")
	flag.StringVar(&f.groupOut, "group-out", "", "per-group timeline CSV (default: <out> with -groups suffix)")
	flag.StringVar(&f.groupSummaryOut, "group-summary-out", "", "group quantile CSV (default: <out> with -group-summary suffix)")
	flag.DurationVar(&f.duration, "duration", 0, "stop watching after this long (0 = until SIGINT)")
	flag.DurationVar(&f.drain, "drain", 30*time.Second, "extra wait after duration for in-flight pods to settle")
	flag.StringVar(&f.promURL, "prom-url", "", "Prometheus base URL for the server-side P99 cross-check (optional)")
	flag.Float64Var(&f.qps, "client-qps", 100, "client-go QPS for the watch client")
	flag.IntVar(&f.burst, "client-burst", 200, "client-go burst for the watch client")
	flag.Parse()
	return f
}

func main() {
	f := parseFlags()
	if f.submitLog == "" {
		log.Fatal("--submit-log is required: without submit_ts there is no scheduling latency to compute")
	}
	if f.summaryOut == "" {
		f.summaryOut = strings.TrimSuffix(f.out, filepath.Ext(f.out)) + "-summary.csv"
	}
	baseOut := strings.TrimSuffix(f.out, filepath.Ext(f.out))
	if f.groupOut == "" {
		f.groupOut = baseOut + "-groups.csv"
	}
	if f.groupSummaryOut == "" {
		f.groupSummaryOut = baseOut + "-group-summary.csv"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; cancel() }()
	if f.duration > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, f.duration+f.drain)
		defer stop()
	}

	config, err := clientcmd.BuildConfigFromFlags("", f.kubeconfig)
	if err != nil {
		log.Fatalf("build Kubernetes config: %v", err)
	}
	config.QPS = float32(f.qps)
	config.Burst = f.burst
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("build Kubernetes clientset: %v", err)
	}

	tracker := profiler.NewTracker()
	log.Printf("profiler: watching ns=%q selector=%q (start me before loadgen)", f.namespace, f.labelSelector)
	if err := profiler.Watch(ctx, cs, profiler.WatchOptions{
		Namespace:     f.namespace,
		LabelSelector: f.labelSelector,
		Resync:        0,
	}, tracker); err != nil && ctx.Err() == nil {
		log.Fatalf("watch pods: %v", err)
	}

	cutoff := time.Now()
	log.Printf("profiler: watch stopped, observed %d pods; joining %s", tracker.Len(), f.submitLog)

	logFile, err := os.Open(f.submitLog)
	if err != nil {
		log.Fatalf("open submit log: %v", err)
	}
	submits, err := profiler.LoadSubmitLog(logFile)
	closeErr := logFile.Close()
	if err != nil {
		log.Fatalf("read submit log: %v", err)
	}
	if closeErr != nil {
		log.Printf("warning: close submit log: %v", closeErr)
	}

	timelines, stats := profiler.Join(submits, tracker.Snapshot(), cutoff)
	report := profiler.Summarize(timelines, profiler.DefaultQuantiles...)
	groups := profiler.AggregateGroups(timelines)
	groupReport := profiler.SummarizeGroups(groups, profiler.DefaultQuantiles...)

	if err := writeCSV(f.out, func(file *os.File) error {
		return profiler.WriteTimelinesCSV(file, timelines)
	}); err != nil {
		log.Fatalf("write timelines csv: %v", err)
	}
	if err := writeCSV(f.summaryOut, func(file *os.File) error {
		return profiler.WriteSummaryCSV(file, report)
	}); err != nil {
		log.Fatalf("write summary csv: %v", err)
	}
	if err := writeCSV(f.groupOut, func(file *os.File) error {
		return profiler.WriteGroupTimelinesCSV(file, groups)
	}); err != nil {
		log.Fatalf("write group timelines csv: %v", err)
	}
	if err := writeCSV(f.groupSummaryOut, func(file *os.File) error {
		return profiler.WriteSummaryCSV(file, groupReport)
	}); err != nil {
		log.Fatalf("write group summary csv: %v", err)
	}

	log.Printf("profiler: submitted=%d matched=%d unobserved=%d unsubmitted=%d censored=%d (%.1f%%)",
		stats.Submitted, stats.Matched, stats.Unobserved, stats.Unsubmitted, stats.Censored,
		report.CensoredRate*100)
	for _, p := range report.Phases {
		log.Printf("profiler: %-12s n=%-5d p50=%v p95=%v p99=%v",
			p.Name, p.Count, p.Quantiles[0.5], p.Quantiles[0.95], p.Quantiles[0.99])
	}
	log.Printf("profiler: wrote %s and %s", f.out, f.summaryOut)
	log.Printf("profiler: groups=%d complete=%d censored=%d; wrote %s and %s",
		groupReport.Total, groupReport.Complete, groupReport.Censored, f.groupOut, f.groupSummaryOut)

	if f.promURL != "" {
		v, err := profiler.InstantQuery(context.Background(), f.promURL, profiler.APIServerP99Query)
		if err != nil {
			log.Printf("warning: prometheus cross-check failed: %v", err)
		} else {
			log.Printf("profiler: apiserver POST pods P99 (server side) = %.3f ms", v*1000)
		}
	}
}

func writeCSV(path string, write func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := write(file); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
