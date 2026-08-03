package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var reservedMetaKeys = map[string]struct{}{
	"schema_version": {}, "run_id": {}, "experiment": {}, "arm": {},
	"status": {}, "started_at": {}, "ended_at": {}, "duration_seconds": {},
	"git_sha": {}, "git_dirty": {}, "git_error": {},
	"hostname": {}, "os": {}, "arch": {}, "cpus": {}, "go_version": {},
	"prometheus_url": {}, "prometheus_step_seconds": {},
	"host_interval_seconds": {}, "host_sampler": {},
}

// NewMetadata creates the run-level provenance written beside every experiment result.
// Controls are deliberately flattened at the top level so analysis/plot.py can group by
// fields such as nodes, scheduler, qps, seed, and optimize without schema-specific code.
func NewMetadata(runID, experiment, arm string, startedAt time.Time, controls map[string]any) (map[string]any, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("run ID must not be empty")
	}
	if strings.TrimSpace(experiment) == "" {
		return nil, errors.New("experiment must not be empty")
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	meta := map[string]any{
		"schema_version": 1,
		"run_id":         runID,
		"experiment":     experiment,
		"status":         "running",
		"started_at":     startedAt.UTC().Format(time.RFC3339Nano),
		"hostname":       hostname,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"cpus":           runtime.NumCPU(),
		"go_version":     runtime.Version(),
	}
	if arm != "" {
		meta["arm"] = arm
	}
	for key, value := range controls {
		if _, reserved := reservedMetaKeys[key]; reserved {
			return nil, fmt.Errorf("metadata key %q is reserved", key)
		}
		if strings.TrimSpace(key) == "" {
			return nil, errors.New("metadata key must not be empty")
		}
		meta[key] = value
	}
	return meta, nil
}

// ParseAssignment parses --meta key=value. JSON scalars keep their type, while ordinary
// unquoted text remains a string: nodes=500 is an integer, optimize=false is a boolean,
// and scheduler=default-scheduler is a string.
func ParseAssignment(text string) (string, any, error) {
	key, raw, ok := strings.Cut(text, "=")
	key = strings.TrimSpace(key)
	raw = strings.TrimSpace(raw)
	if !ok || key == "" || raw == "" {
		return "", nil, fmt.Errorf("expected non-empty key=value, got %q", text)
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err == nil {
		return key, value, nil
	}
	return key, raw, nil
}

// GitInfo returns the exact source revision and whether tracked or untracked changes
// existed when the run started. A dirty tree does not invalidate a run, but it must be
// visible in provenance.
func GitInfo(ctx context.Context, dir string) (sha string, dirty bool, err error) {
	rev := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	rev.Dir = dir
	out, err := rev.Output()
	if err != nil {
		return "", false, fmt.Errorf("git rev-parse: %w", err)
	}
	status := exec.CommandContext(ctx, "git", "status", "--porcelain")
	status.Dir = dir
	statusOut, err := status.Output()
	if err != nil {
		return "", false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(out)), len(bytes.TrimSpace(statusOut)) != 0, nil
}

// FinishMetadata records the actual observation window and terminal collector status.
func FinishMetadata(meta map[string]any, endedAt time.Time, status string) {
	meta["status"] = status
	meta["ended_at"] = endedAt.UTC().Format(time.RFC3339Nano)
	if raw, ok := meta["started_at"].(string); ok {
		if startedAt, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			meta["duration_seconds"] = endedAt.Sub(startedAt).Seconds()
		}
	}
}

// WriteMetadata atomically replaces a metadata file so a crash cannot leave truncated
// JSON. The collector writes status=running at start and updates it when collection ends.
func WriteMetadata(path string, meta map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create metadata temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(meta); err != nil {
		tmp.Close()
		return fmt.Errorf("encode metadata: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync metadata: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close metadata: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace metadata: %w", err)
	}
	return nil
}
