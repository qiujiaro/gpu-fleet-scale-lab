package topogang

import (
	"sync"
	"time"

	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
)

var (
	registerMetricsOnce sync.Once

	preFilterNodesScanned = metrics.NewHistogram(
		&metrics.HistogramOpts{
			Namespace: "topogang", Name: "prefilter_nodes_scanned",
			Help:           "Number of scheduler snapshot nodes inspected by one TopoGang PreFilter call.",
			Buckets:        []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096},
			StabilityLevel: metrics.ALPHA,
		},
	)
	registryLockWait = metrics.NewHistogram(
		&metrics.HistogramOpts{
			Namespace: "topogang", Name: "registry_lock_wait_seconds",
			Help:           "Time spent waiting to acquire the shared TopoGang Registry mutex.",
			Buckets:        metrics.ExponentialBuckets(0.000001, 2, 20),
			StabilityLevel: metrics.ALPHA,
		},
	)
	podGroupWait = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Namespace: "topogang", Name: "podgroup_wait_seconds",
			Help:           "Gang Permit barrier wait, split by allowed or rejected outcome.",
			Buckets:        metrics.ExponentialBuckets(0.001, 2, 18),
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"outcome"},
	)
	gangRejects = metrics.NewCounterVec(
		&metrics.CounterOpts{
			Namespace: "topogang", Name: "gang_reject_total",
			Help:           "Number of TopoGang group rejections by bounded-cardinality reason.",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"reason"},
	)
	waitingPodsIterated = metrics.NewHistogram(
		&metrics.HistogramOpts{
			Namespace: "topogang", Name: "waiting_pods_iterated",
			Help:           "Number of process-wide waiting Pods inspected while allowing or rejecting a gang.",
			Buckets:        metrics.ExponentialBuckets(1, 2, 14),
			StabilityLevel: metrics.ALPHA,
		},
	)
)

func registerMetrics() {
	registerMetricsOnce.Do(func() {
		legacyregistry.MustRegister(
			preFilterNodesScanned,
			registryLockWait,
			podGroupWait,
			gangRejects,
			waitingPodsIterated,
		)
	})
}

func observeRegistryLock(start time.Time) {
	registryLockWait.Observe(time.Since(start).Seconds())
}

func observePodGroupWait(outcome string, start, end time.Time) {
	if start.IsZero() || end.Before(start) {
		return
	}
	podGroupWait.WithLabelValues(outcome).Observe(end.Sub(start).Seconds())
}
