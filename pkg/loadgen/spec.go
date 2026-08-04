package loadgen

import (
	"errors"
	"time"
)

// WorkloadSpec contains the knobs used by the load generator.
//
// Keep configuration here; the scheduling and rate-limiting algorithms belong
// in Run so they can be tested without going through the CLI.
type WorkloadSpec struct {
	Namespace     string
	Duration      time.Duration
	MaxQPS        float64
	Burst         int
	Workers       int
	GPU           int
	SchedulerName string
	GangSize      int
	MaxGangs      int
	MaxPods       int
	RunID         string
	Arrival       ArrivalModel
}

// Validate checks configuration before any goroutines or API requests start.
// TODO(Day2): implement validation for rates, burst, workers, duration, and Arrival.
func (s WorkloadSpec) Validate() error {
	if s.MaxQPS <= 0 {
		return ErrInvalidMaxQPS
	}
	if s.Burst <= 0 {
		return ErrInvalidBurst
	}
	if s.Workers <= 0 {
		return ErrInvalidWorkers
	}
	if s.Duration <= 0 {
		return ErrInvalidDuration
	}
	if s.Arrival == nil {
		return ErrInvalidArrivalModel
	}
	if s.GangSize < 1 {
		return ErrInvalidGangSize
	}
	if s.GangSize > 1 && s.RunID == "" {
		return ErrMissingRunID
	}
	if s.MaxGangs < 0 {
		return ErrInvalidMaxGangs
	}
	if s.MaxPods < 0 {
		return ErrInvalidMaxPods
	}
	if s.MaxGangs > 0 && s.GangSize <= 1 {
		return ErrMaxGangsWithoutGang
	}
	if s.MaxGangs > 0 && s.MaxPods > 0 {
		return ErrConflictingPodLimits
	}
	return nil

}

// package-level errors for validation failures
var (
	ErrInvalidMaxQPS        = errors.New("invalid MaxQPS: must be > 0")
	ErrInvalidBurst         = errors.New("invalid Burst: must be > 0")
	ErrInvalidWorkers       = errors.New("invalid Workers: must be > 0")
	ErrInvalidDuration      = errors.New("invalid Duration: must be > 0")
	ErrInvalidArrivalModel  = errors.New("invalid Arrival model: must be non-nil")
	ErrInvalidGangSize      = errors.New("invalid GangSize: must be >= 1")
	ErrInvalidMaxGangs      = errors.New("invalid MaxGangs: must be >= 0")
	ErrInvalidMaxPods       = errors.New("invalid MaxPods: must be >= 0")
	ErrMaxGangsWithoutGang  = errors.New("MaxGangs requires GangSize > 1")
	ErrConflictingPodLimits = errors.New("MaxPods and MaxGangs cannot both be set")
	ErrMissingRunID         = errors.New("RunID is required when GangSize > 1")
)
