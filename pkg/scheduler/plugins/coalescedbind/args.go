package coalescedbind

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultWindow        = 5 * time.Millisecond
	defaultMaxBatch      = 16
	defaultMaxInFlight   = 8
	defaultQueueCapacity = 256
)

type Args struct {
	Window        metav1.Duration `json:"window,omitempty"`
	MaxBatch      int32           `json:"maxBatch,omitempty"`
	MaxInFlight   int32           `json:"maxInFlight,omitempty"`
	QueueCapacity int32           `json:"queueCapacity,omitempty"`
}

func defaultArgs() Args {
	return Args{
		Window:        metav1.Duration{Duration: defaultWindow},
		MaxBatch:      defaultMaxBatch,
		MaxInFlight:   defaultMaxInFlight,
		QueueCapacity: defaultQueueCapacity,
	}
}

func (a Args) validate() error {
	if a.Window.Duration <= 0 {
		return fmt.Errorf("window must be greater than zero")
	}
	if a.MaxBatch <= 0 {
		return fmt.Errorf("maxBatch must be greater than zero")
	}
	if a.MaxInFlight <= 0 {
		return fmt.Errorf("maxInFlight must be greater than zero")
	}
	if a.QueueCapacity < a.MaxBatch {
		return fmt.Errorf("queueCapacity must be at least maxBatch")
	}
	return nil
}
