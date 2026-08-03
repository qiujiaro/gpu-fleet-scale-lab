package telemetry

import (
	"bytes"
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"time"
)

type cancelingHostSource struct {
	cancel context.CancelFunc
}

func (s cancelingHostSource) Sample(context.Context) (HostSample, error) {
	s.cancel()
	return HostSample{
		Timestamp:  time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
		CPUPercent: 12.5,
		MemoryMB:   1024,
	}, nil
}

func TestCollectHostWritesAnalysisSchema(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	if err := CollectHost(ctx, cancelingHostSource{cancel: cancel}, time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want header plus one sample: %q", len(rows), out.String())
	}
	if got := strings.Join(rows[0], ","); got != "ts,cpu_percent,mem_mb" {
		t.Fatalf("unexpected header %q", got)
	}
	if rows[1][1] != "12.500" || rows[1][2] != "1024.000" {
		t.Fatalf("unexpected sample %#v", rows[1])
	}
}
