package telemetry

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

const (
	APIServerP99Metric = "apiserver_p99_seconds"
	APFInQueueMetric   = "apf_inqueue"
	HTTP429Metric      = "http_429_total"
	PodCreateMetric    = "pod_create_success_total"
	PodBindingMetric   = "pod_binding_success_total"
)

type PromQuery struct {
	Name string
	Expr string
}

var DefaultPromQueries = []PromQuery{
	{
		Name: APIServerP99Metric,
		Expr: `histogram_quantile(0.99, sum(rate(apiserver_request_duration_seconds_bucket{verb="POST",resource="pods"}[30s])) by (le))`,
	},
	{
		Name: APFInQueueMetric,
		Expr: `sum(apiserver_flowcontrol_current_inqueue_requests)`,
	},
	{
		Name: HTTP429Metric,
		Expr: `sum(apiserver_request_total{code="429"})`,
	},
	{
		Name: PodCreateMetric,
		Expr: `sum(apiserver_request_total{verb="POST",resource="pods",subresource="",code=~"2.."}) or vector(0)`,
	},
	{
		Name: PodBindingMetric,
		Expr: `sum(apiserver_request_total{verb="POST",resource="pods",subresource="binding",code=~"2.."}) or vector(0)`,
	},
}

type PromSample struct {
	Timestamp time.Time
	Metric    string
	Labels    map[string]string
	Value     float64
}

type rangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string   `json:"metric"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

type PromClient struct {
	HTTPClient *http.Client
}

func (c PromClient) RangeQuery(
	ctx context.Context,
	baseURL string,
	query PromQuery,
	start, end time.Time,
	step time.Duration,
) ([]PromSample, error) {
	if query.Name == "" || query.Expr == "" {
		return nil, fmt.Errorf("prometheus query name and expression are required")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("prometheus range end must be after start")
	}
	if step <= 0 {
		return nil, fmt.Errorf("prometheus range step must be positive")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse prometheus url: %w", err)
	}
	u.Path = "/api/v1/query_range"
	u.RawQuery = url.Values{
		"query": {query.Expr},
		"start": {strconv.FormatFloat(float64(start.UnixNano())/1e9, 'f', 3, 64)},
		"end":   {strconv.FormatFloat(float64(end.UnixNano())/1e9, 'f', 3, 64)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', 3, 64)},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build prometheus range request: %w", err)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query prometheus: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("prometheus returned HTTP %s", resp.Status)
	}

	var decoded rangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode prometheus response: %w", err)
	}
	if decoded.Status != "success" {
		return nil, fmt.Errorf("prometheus %s: %s", decoded.ErrorType, decoded.Error)
	}
	if decoded.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("prometheus returned result type %q, want matrix", decoded.Data.ResultType)
	}

	var samples []PromSample
	for _, series := range decoded.Data.Result {
		for _, pair := range series.Values {
			if len(pair) != 2 {
				return nil, fmt.Errorf("unexpected prometheus sample shape %v", pair)
			}
			var timestamp float64
			var rawValue string
			if err := json.Unmarshal(pair[0], &timestamp); err != nil {
				return nil, fmt.Errorf("decode prometheus timestamp: %w", err)
			}
			if err := json.Unmarshal(pair[1], &rawValue); err != nil {
				return nil, fmt.Errorf("decode prometheus value: %w", err)
			}
			value, err := strconv.ParseFloat(rawValue, 64)
			if err != nil {
				return nil, fmt.Errorf("parse prometheus value %q: %w", rawValue, err)
			}
			samples = append(samples, PromSample{
				Timestamp: time.Unix(0, int64(timestamp*1e9)).UTC(),
				Metric:    query.Name,
				Labels:    series.Metric,
				Value:     value,
			})
		}
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Timestamp.Equal(samples[j].Timestamp) {
			return samples[i].Metric < samples[j].Metric
		}
		return samples[i].Timestamp.Before(samples[j].Timestamp)
	})
	return samples, nil
}

func WritePrometheusCSV(w io.Writer, samples []PromSample) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"ts", "metric", "labels", "value"}); err != nil {
		return err
	}
	for _, sample := range samples {
		labels, err := json.Marshal(sample.Labels)
		if err != nil {
			return fmt.Errorf("encode prometheus labels: %w", err)
		}
		if err := cw.Write([]string{
			sample.Timestamp.UTC().Format(time.RFC3339Nano),
			sample.Metric,
			string(labels),
			strconv.FormatFloat(sample.Value, 'g', -1, 64),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}

func WriteAPIServerCSV(w io.Writer, samples []PromSample) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"ts", "p99_ms"}); err != nil {
		return err
	}
	for _, sample := range samples {
		if sample.Metric != APIServerP99Metric || math.IsNaN(sample.Value) {
			continue
		}
		if err := cw.Write([]string{
			sample.Timestamp.UTC().Format(time.RFC3339Nano),
			strconv.FormatFloat(sample.Value*1000, 'f', 3, 64),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}

func WritePressureCSV(w io.Writer, samples []PromSample) error {
	var apfPeak float64
	var counterValues []float64
	apfAvailable := false
	for _, sample := range samples {
		switch sample.Metric {
		case APFInQueueMetric:
			if !math.IsNaN(sample.Value) {
				apfAvailable = true
				if sample.Value > apfPeak {
					apfPeak = sample.Value
				}
			}
		case HTTP429Metric:
			if !math.IsNaN(sample.Value) {
				counterValues = append(counterValues, sample.Value)
			}
		}
	}
	http429Available := len(counterValues) > 0
	http429Delta := 0.0
	if len(counterValues) >= 2 {
		http429Delta = counterValues[len(counterValues)-1] - counterValues[0]
		if http429Delta < 0 {
			http429Delta = counterValues[len(counterValues)-1]
		}
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"metric", "value", "available"}); err != nil {
		return err
	}
	http429Value := ""
	if http429Available {
		http429Value = strconv.FormatFloat(http429Delta, 'f', 0, 64)
	}
	apfValue := ""
	if apfAvailable {
		apfValue = strconv.FormatFloat(apfPeak, 'f', 3, 64)
	}
	for _, row := range [][]string{
		{"http_429_total", http429Value, strconv.FormatBool(http429Available)},
		{"apf_inqueue_peak", apfValue, strconv.FormatBool(apfAvailable)},
	} {
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	return cw.Error()
}
