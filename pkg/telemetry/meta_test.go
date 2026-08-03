package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAssignmentPreservesScalarTypes(t *testing.T) {
	tests := []struct {
		input string
		key   string
		want  any
	}{
		{"nodes=500", "nodes", json.Number("500")},
		{"optimize=false", "optimize", false},
		{"qps=50.5", "qps", json.Number("50.5")},
		{"scheduler=default-scheduler", "scheduler", "default-scheduler"},
	}
	for _, test := range tests {
		key, got, err := ParseAssignment(test.input)
		if err != nil {
			t.Fatalf("ParseAssignment(%q): %v", test.input, err)
		}
		if key != test.key || got != test.want {
			t.Fatalf("ParseAssignment(%q) = (%q, %#v), want (%q, %#v)",
				test.input, key, got, test.key, test.want)
		}
	}
}

func TestNewMetadataFlattensControlsAndRejectsReservedKeys(t *testing.T) {
	start := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	meta, err := NewMetadata("run-1", "exp1", "default", start, map[string]any{
		"nodes":     500,
		"scheduler": "default-scheduler",
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta["nodes"] != 500 || meta["scheduler"] != "default-scheduler" {
		t.Fatalf("controls were not flattened: %#v", meta)
	}
	if meta["status"] != "running" || meta["arm"] != "default" {
		t.Fatalf("missing standard metadata: %#v", meta)
	}
	if _, err := NewMetadata("run-1", "exp1", "", start, map[string]any{"run_id": "other"}); err == nil {
		t.Fatal("expected reserved-key error")
	}
}

func TestWriteMetadataProducesCompleteJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "run-meta.json")
	meta := map[string]any{"run_id": "run-1", "nodes": 500}
	if err := WriteMetadata(path, meta); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("metadata is not valid JSON: %v\n%s", err, raw)
	}
	if decoded["run_id"] != "run-1" || decoded["nodes"] != float64(500) {
		t.Fatalf("unexpected metadata: %#v", decoded)
	}
}
