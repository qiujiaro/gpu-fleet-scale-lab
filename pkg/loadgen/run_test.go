package loadgen

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
)

type countingSubmitter struct{}

func (countingSubmitter) Create(_ context.Context, req SubmitRequest) (SubmitResult, error) {
	return SubmitResult{Name: req.Name, UID: fmt.Sprintf("uid-%s", req.Name)}, nil
}

func TestGroupForSequence(t *testing.T) {
	tests := []struct {
		sequence   int
		wantGroup  string
		wantMember int
	}{
		{sequence: 0, wantGroup: "run-gang-000000", wantMember: 0},
		{sequence: 3, wantGroup: "run-gang-000000", wantMember: 3},
		{sequence: 4, wantGroup: "run-gang-000001", wantMember: 0},
	}
	for _, test := range tests {
		group, member := groupForSequence("run", 4, test.sequence)
		if group != test.wantGroup || member != test.wantMember {
			t.Fatalf("sequence %d: got (%q,%d), want (%q,%d)",
				test.sequence, group, member, test.wantGroup, test.wantMember)
		}
	}
}

func TestGroupForSequence_Disabled(t *testing.T) {
	group, member := groupForSequence("", 1, 9)
	if group != "" || member != 0 {
		t.Fatalf("gang disabled: got (%q,%d)", group, member)
	}
}

func TestWorkloadSpec_MaxGangsValidation(t *testing.T) {
	base := WorkloadSpec{
		Duration: time.Second, MaxQPS: 1, Burst: 1, Workers: 1,
		GangSize: 4, RunID: "run", Arrival: Constant{RatePerSec: 1},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid gang spec: %v", err)
	}
	base.MaxGangs = -1
	if err := base.Validate(); err != ErrInvalidMaxGangs {
		t.Fatalf("negative MaxGangs error=%v, want %v", err, ErrInvalidMaxGangs)
	}
	base.MaxGangs = 1
	base.GangSize = 1
	if err := base.Validate(); err != ErrMaxGangsWithoutGang {
		t.Fatalf("MaxGangs without gang error=%v, want %v", err, ErrMaxGangsWithoutGang)
	}
}

func TestRun_MaxGangsStopsOnCompleteBoundary(t *testing.T) {
	var output bytes.Buffer
	recorder := NewRecorder(&output)
	stats, err := Run(context.Background(), countingSubmitter{}, WorkloadSpec{
		Namespace: "default", Duration: time.Second, MaxQPS: 1000, Burst: 16, Workers: 4,
		GangSize: 4, MaxGangs: 2, RunID: "run", Arrival: Constant{RatePerSec: 1000},
	}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if stats.Succeeded != 8 || stats.Failed != 0 {
		t.Fatalf("stats=%+v, want exactly 8 successful Pods", stats)
	}
}
