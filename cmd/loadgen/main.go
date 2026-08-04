// Command loadgen submits Pods/PodGroups to the apiserver at a controlled rate.
//
// Day 2 goal: implement constant/poisson/burst arrival models + token-bucket rate
// limiting + 429 counting + Recorder. The core logic must be hand-written (see
// pkg/loadgen). This file only holds CLI wiring and may be vibe-coded.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/qiujiaro/gpu-fleet-scale-lab/pkg/loadgen"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type flags struct {
	kubeconfig         string
	namespace          string
	arrival            string // constant | poisson | burst
	qps                float64
	burst              int
	clientQPS          float64
	clientBurst        int
	durationSec        int
	maxPods            int
	gpu                int
	schedulerName      string
	out                string
	seed               int64
	gangSize           int
	maxGangs           int
	runID              string
	simulatedColdStart bool
	spikeCount         int
	burstAt            time.Duration
	preloadQPS         float64
}

type kubeSubmitter struct {
	cs                 kubernetes.Interface
	simulatedColdStart bool
}

func (s kubeSubmitter) Create(ctx context.Context, req loadgen.SubmitRequest) (loadgen.SubmitResult, error) {
	gpu := *resource.NewQuantity(int64(req.GPU), resource.DecimalSI)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.Name,
			Namespace:    req.Namespace,
			Labels: map[string]string{
				"exp2.dev/run-id": req.RunID,
			},
		},
		Spec: corev1.PodSpec{
			SchedulerName: req.SchedulerName,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:  "workload",
				Image: "registry.k8s.io/pause:3.9",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): gpu,
					},
					Limits: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): gpu,
					},
				},
			}},
		},
	}
	if s.simulatedColdStart {
		pod.Labels["gpu-lab/simulated-cold-start"] = "true"
	}
	if req.GroupID != "" {
		pod.Labels["topogang.dev/pod-group"] = req.GroupID
		pod.Labels["topogang.dev/min-member"] = fmt.Sprintf("%d", req.MinMember)
		pod.Labels["exp2.dev/member-index"] = fmt.Sprintf("%d", req.MemberIndex)
	}
	created, err := s.cs.CoreV1().Pods(req.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return loadgen.SubmitResult{}, err
	}
	return loadgen.SubmitResult{Name: created.Name, UID: string(created.UID)}, nil
}

func parseFlags() flags {
	var f flags
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	flag.StringVar(&f.kubeconfig, "kubeconfig", kubeconfig, "path to kubeconfig")
	flag.StringVar(&f.namespace, "namespace", "default", "namespace for submitted Pods")
	flag.StringVar(&f.arrival, "arrival", "constant", "arrival model: constant|poisson|burst")
	flag.Float64Var(&f.qps, "qps", 30, "target submission QPS (token-bucket rate)")
	flag.IntVar(&f.burst, "burst", 50, "token-bucket burst")
	flag.Float64Var(&f.clientQPS, "client-qps", 0, "client-go QPS; 0 uses --qps")
	flag.IntVar(&f.clientBurst, "client-burst", 0, "client-go burst; 0 uses --burst")
	flag.IntVar(&f.durationSec, "duration", 60, "run duration seconds")
	flag.IntVar(&f.maxPods, "max-pods", 0, "stop after exactly this many Pods; 0 uses duration/max-gangs")
	flag.IntVar(&f.gpu, "gpu", 1, "nvidia.com/gpu request per pod")
	flag.StringVar(&f.schedulerName, "scheduler-name", "default-scheduler", "spec.schedulerName")
	flag.StringVar(&f.out, "out", "experiments/diagnostics/local/run.jsonl", "submit-log output path")
	flag.Int64Var(&f.seed, "seed", 42, "RNG seed for reproducibility")
	flag.IntVar(&f.gangSize, "gang-size", 1, "pods per gang; 1 disables gang labels")
	flag.IntVar(&f.maxGangs, "max-gangs", 0, "stop after this many complete gangs; 0 uses duration only")
	flag.StringVar(&f.runID, "run-id", "", "stable run identifier (required when --gang-size > 1)")
	flag.BoolVar(&f.simulatedColdStart, "simulated-cold-start", false, "label Pods for the KWOK simulated cold-start Stage")
	flag.IntVar(&f.spikeCount, "spike-count", 0, "Pods in the one-time burst; 0 uses --burst")
	flag.DurationVar(&f.burstAt, "burst-at", 0, "time of one-time burst; 0 uses half of --duration")
	flag.Float64Var(&f.preloadQPS, "preload-qps", -1, "steady rate before a burst; -1 uses --qps, 0 disables preload")
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

	config, err := clientcmd.BuildConfigFromFlags("", f.kubeconfig)
	if err != nil {
		log.Fatalf("build Kubernetes config: %v", err)
	}
	clientQPS := f.clientQPS
	if clientQPS == 0 {
		clientQPS = f.qps
	}
	clientBurst := f.clientBurst
	if clientBurst == 0 {
		clientBurst = f.burst
	}
	config.QPS = float32(clientQPS)
	config.Burst = clientBurst
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("build Kubernetes clientset: %v", err)
	}

	var arrival loadgen.ArrivalModel
	switch f.arrival {
	case "constant":
		arrival = loadgen.Constant{RatePerSec: f.qps}
	case "poisson":
		arrival = loadgen.Poisson{Lambda: f.qps, R: rand.New(rand.NewSource(f.seed))}
	case "burst":
		spikeCount := f.spikeCount
		if spikeCount == 0 {
			spikeCount = f.burst
		}
		burstAt := f.burstAt
		if burstAt == 0 {
			burstAt = time.Duration(f.durationSec) * time.Second / 2
		}
		preloadQPS := f.preloadQPS
		if preloadQPS < 0 {
			preloadQPS = f.qps
		}
		arrival = &loadgen.Burst{
			SteadyRatePerSec: preloadQPS,
			At:               burstAt,
			SpikeCount:       spikeCount,
		}
	default:
		log.Fatal(fmt.Errorf("unsupported arrival model %q", f.arrival))
	}
	spec := loadgen.WorkloadSpec{
		Namespace:     f.namespace,
		Duration:      time.Duration(f.durationSec) * time.Second,
		MaxQPS:        f.qps,
		Burst:         f.burst,
		Workers:       f.burst,
		GPU:           f.gpu,
		SchedulerName: f.schedulerName,
		GangSize:      f.gangSize,
		MaxGangs:      f.maxGangs,
		MaxPods:       f.maxPods,
		RunID:         f.runID,
		Arrival:       arrival,
	}
	if err := os.MkdirAll(filepath.Dir(f.out), 0o755); err != nil {
		log.Fatalf("create output directory: %v", err)
	}
	out, err := os.Create(f.out)
	if err != nil {
		log.Fatalf("create submit log: %v", err)
	}
	recorder := loadgen.NewRecorder(out)

	stats, err := loadgen.Run(ctx, kubeSubmitter{
		cs:                 cs,
		simulatedColdStart: f.simulatedColdStart,
	}, spec, recorder)
	if closeErr := recorder.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("flush submit log: %w", closeErr)
	}
	if closeErr := out.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("close submit log: %w", closeErr)
	}
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("loadgen: arrival=%s qps=%.1f duration=%ds gpu=%d scheduler=%s out=%s seed=%d",
		f.arrival, f.qps, f.durationSec, f.gpu, f.schedulerName, f.out, f.seed)
	log.Printf("loadgen: attempted=%d succeeded=%d failed=%d rate-limited=%d",
		stats.Attempted, stats.Succeeded, stats.Failed, stats.RateLimited)
	log.Printf("loadgen: first-to-last-success=%.3fs", stats.SuccessSpan().Seconds())
}
