package coalescedbind

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

const Name = "CoalescedBind"

type CoalescedBind struct {
	client  kubernetes.Interface
	batcher Batcher
}

var _ framework.BindPlugin = &CoalescedBind{}

func New(_ context.Context, obj runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	if handle == nil || handle.ClientSet() == nil {
		return nil, fmt.Errorf("framework handle and clientset are required")
	}
	args := defaultArgs()
	if err := frameworkruntime.DecodeInto(obj, &args); err != nil {
		return nil, fmt.Errorf("decode %s args: %w", Name, err)
	}
	if err := args.validate(); err != nil {
		return nil, fmt.Errorf("validate %s args: %w", Name, err)
	}
	p := &CoalescedBind{client: handle.ClientSet()}
	batcher, err := newBatcher(
		args.Window.Duration,
		int(args.MaxBatch),
		int(args.MaxInFlight),
		int(args.QueueCapacity),
		p.bindOne,
	)
	if err != nil {
		return nil, err
	}
	p.batcher = batcher
	return p, nil
}

func (p *CoalescedBind) Name() string { return Name }

func (p *CoalescedBind) Bind(ctx context.Context, _ *framework.CycleState, pod *v1.Pod, nodeName string) *framework.Status {
	if pod == nil {
		return framework.NewStatus(framework.Error, "pod is nil")
	}
	if p.batcher == nil {
		return framework.NewStatus(framework.Error, "batcher is nil")
	}
	if err := p.batcher.Submit(ctx, BindRequest{Pod: pod.DeepCopy(), NodeName: nodeName}); err != nil {
		return framework.AsStatus(err)
	}
	return nil
}

func (p *CoalescedBind) bindOne(ctx context.Context, req BindRequest) error {
	return p.client.CoreV1().Pods(req.Pod.Namespace).Bind(ctx, &v1.Binding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Pod.Name,
			Namespace: req.Pod.Namespace,
			UID:       req.Pod.UID,
		},
		Target: v1.ObjectReference{Kind: "Node", Name: req.NodeName},
	}, metav1.CreateOptions{})
}
