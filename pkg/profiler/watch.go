// Informer wiring. This file is the vibe-codable half of Day 3: it must be reviewed
// but the logic is standard client-go boilerplate, not a measurement decision.
//
// Reference:
//   - sample-controller (informer + handler pattern):
//     https://github.com/kubernetes/sample-controller/blob/master/controller.go
//   - SharedInformerFactory / WithTweakListOptions:
//     https://pkg.go.dev/k8s.io/client-go/informers#NewSharedInformerFactoryWithOptions
//   - ResourceEventHandlerFuncs / WaitForCacheSync:
//     https://pkg.go.dev/k8s.io/client-go/tools/cache#ResourceEventHandlerFuncs
package profiler

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// WatchOptions configures the Pod informer.
type WatchOptions struct {
	Namespace string
	// LabelSelector narrows the watch to this run's pods. Empty means every pod in the
	// namespace, which will also pick up pods from earlier runs still lying around.
	LabelSelector string
	// Resync of 0 disables periodic resync. Prefer 0: a resync replays every cached pod
	// through UpdateFunc with a fresh ObservedAt, which is exactly the stale-overwrite
	// case Tracker.Observe has to defend against.
	Resync time.Duration
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time
}

// Watch runs a Pod informer until ctx is cancelled, feeding every observation into
// the tracker. It returns once the informer has stopped.
//
// Start this BEFORE loadgen submits — a watch established after creation misses the
// early events and silently biases scheduling latency downward.
func Watch(ctx context.Context, cs kubernetes.Interface, opts WatchOptions, tracker *Tracker) error {
	if cs == nil {
		return fmt.Errorf("clientset must not be nil")
	}
	if tracker == nil {
		return fmt.Errorf("tracker must not be nil")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		cs,
		opts.Resync,
		informers.WithNamespace(opts.Namespace),
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.LabelSelector = opts.LabelSelector
		}),
	)
	podInformer := factory.Core().V1().Pods().Informer()

	observe := func(obj interface{}) {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			// Tombstone on delete; nothing to measure from it.
			return
		}
		tracker.Observe(ExtractState(pod, now()))
	}

	if _, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    observe,
		UpdateFunc: func(_, newObj interface{}) { observe(newObj) },
	}); err != nil {
		return fmt.Errorf("add pod event handler: %w", err)
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced) {
		return fmt.Errorf("pod informer cache did not sync")
	}

	<-ctx.Done()
	return nil
}
