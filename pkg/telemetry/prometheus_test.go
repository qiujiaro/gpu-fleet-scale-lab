package telemetry

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRangeQueryAndAnalysisExports(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("query") != "up" || r.URL.Query().Get("step") != "5.000" {
			t.Fatalf("unexpected query parameters: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{"resultType":"matrix","result":[{
				"metric":{"job":"apiserver"},
				"values":[[1000,"0.010"],[1005,"0.025"]]
			}]}
		}`))
	}))
	defer server.Close()

	client := PromClient{HTTPClient: server.Client()}
	samples, err := client.RangeQuery(
		context.Background(),
		server.URL,
		PromQuery{Name: APIServerP99Metric, Expr: "up"},
		time.Unix(1000, 0),
		time.Unix(1010, 0),
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[1].Value != 0.025 {
		t.Fatalf("unexpected samples: %#v", samples)
	}

	var out bytes.Buffer
	if err := WriteAPIServerCSV(&out, samples); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "ts,p99_ms") || !strings.Contains(got, "25.000") {
		t.Fatalf("unexpected apiserver CSV:\n%s", got)
	}
}

func TestWritePressureCSVUsesCounterDeltaAndQueuePeak(t *testing.T) {
	samples := []PromSample{
		{Metric: HTTP429Metric, Value: 10},
		{Metric: HTTP429Metric, Value: 14},
		{Metric: APFInQueueMetric, Value: 3},
		{Metric: APFInQueueMetric, Value: 9},
	}
	var out bytes.Buffer
	if err := WritePressureCSV(&out, samples); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "http_429_total,4") || !strings.Contains(got, "apf_inqueue_peak,9.000") {
		t.Fatalf("unexpected pressure CSV:\n%s", got)
	}
}
