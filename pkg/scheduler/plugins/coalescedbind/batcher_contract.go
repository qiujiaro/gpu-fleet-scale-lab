// Package coalescedbind provides the Day 6 BindPlugin adapter.
package coalescedbind

import (
	"context"

	v1 "k8s.io/api/core/v1"
)

type BindRequest struct {
	Pod      *v1.Pod
	NodeName string
}

type Batcher interface {
	Submit(context.Context, BindRequest) error
	Close() error
}

type bindFn func(context.Context, BindRequest) error
