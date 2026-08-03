package telemetry

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// HostSample is a lightweight whole-host signal. mem_mb is the sum of resident memory
// reported by ps; shared pages can be counted more than once, so it is a stable
// comparison signal rather than a physical-memory accounting claim.
type HostSample struct {
	Timestamp  time.Time
	CPUPercent float64
	MemoryMB   float64
}

type HostSource interface {
	Sample(context.Context) (HostSample, error)
}

// PSHostSource uses one portable ps invocation per sample on macOS/Linux. Total process
// CPU is divided by logical CPU count so cpu_percent stays on a 0–100 host scale.
type PSHostSource struct{}

func (PSHostSource) Sample(ctx context.Context) (HostSample, error) {
	cmd := exec.CommandContext(ctx, "ps", "-A", "-o", "%cpu=", "-o", "rss=")
	out, err := cmd.Output()
	if err != nil {
		return HostSample{}, fmt.Errorf("sample host with ps: %w", err)
	}
	var cpu, rssKB float64
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		c, errCPU := strconv.ParseFloat(strings.ReplaceAll(fields[0], ",", "."), 64)
		rss, errRSS := strconv.ParseFloat(fields[1], 64)
		if errCPU != nil || errRSS != nil {
			continue
		}
		cpu += c
		rssKB += rss
	}
	if err := scanner.Err(); err != nil {
		return HostSample{}, fmt.Errorf("parse ps output: %w", err)
	}
	if runtime.NumCPU() > 0 {
		cpu /= float64(runtime.NumCPU())
	}
	return HostSample{
		Timestamp:  time.Now().UTC(),
		CPUPercent: cpu,
		MemoryMB:   rssKB / 1024,
	}, nil
}

// CollectHost writes an immediate sample and then one row per interval until ctx ends.
func CollectHost(ctx context.Context, source HostSource, interval time.Duration, w io.Writer) error {
	if source == nil {
		return fmt.Errorf("host source must not be nil")
	}
	if interval <= 0 {
		return fmt.Errorf("host interval must be positive")
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"ts", "cpu_percent", "mem_mb"}); err != nil {
		return fmt.Errorf("write host csv header: %w", err)
	}
	writeSample := func() error {
		sample, err := source.Sample(ctx)
		if err != nil {
			return err
		}
		if err := cw.Write([]string{
			sample.Timestamp.UTC().Format(time.RFC3339Nano),
			strconv.FormatFloat(sample.CPUPercent, 'f', 3, 64),
			strconv.FormatFloat(sample.MemoryMB, 'f', 3, 64),
		}); err != nil {
			return fmt.Errorf("write host sample: %w", err)
		}
		cw.Flush()
		return cw.Error()
	}
	if err := writeSample(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := writeSample(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}
