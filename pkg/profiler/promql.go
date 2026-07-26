// Prometheus cross-check. Vibe-codable HTTP boilerplate; the *query* is the part worth
// thinking about (Day 3 reading question #2: client-observed vs server-reported P99).
//
// Reference:
//   - HTTP API /api/v1/query response shape:
//     https://prometheus.io/docs/prometheus/latest/querying/api/#instant-queries
//   - histogram_quantile:
//     https://prometheus.io/docs/prometheus/latest/querying/functions/#histogram_quantile
package profiler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// APIServerP99Query is the server-side counterpart of the profiler's scheduling P99.
// Note what it does and does not cover: it is apiserver request latency, not
// scheduler queueing time, so the two numbers should be the same order of magnitude
// but are not expected to match.
const APIServerP99Query = `histogram_quantile(0.99, sum(rate(apiserver_request_duration_seconds_bucket{verb="POST",resource="pods"}[5m])) by (le))`

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"` // [ <unix_time>, "<sample_value>" ]
		} `json:"result"`
	} `json:"data"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

// InstantQuery runs a PromQL instant query and returns the first scalar sample.
// Returns an error if the query yields no series, so a missing metric can't be
// silently reported as 0.
func InstantQuery(ctx context.Context, baseURL, query string) (float64, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return 0, fmt.Errorf("parse prometheus url: %w", err)
	}
	u.Path = "/api/v1/query"
	u.RawQuery = url.Values{"query": {query}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("build prometheus request: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("query prometheus: %w", err)
	}
	defer resp.Body.Close()

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return 0, fmt.Errorf("decode prometheus response: %w", err)
	}
	if pr.Status != "success" {
		return 0, fmt.Errorf("prometheus %s: %s", pr.ErrorType, pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return 0, fmt.Errorf("prometheus returned no series for query %q", query)
	}
	v := pr.Data.Result[0].Value
	if len(v) != 2 {
		return 0, fmt.Errorf("unexpected prometheus sample shape %v", v)
	}
	var raw string
	if err := json.Unmarshal(v[1], &raw); err != nil {
		return 0, fmt.Errorf("decode prometheus sample: %w", err)
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse prometheus sample %q: %w", raw, err)
	}
	return f, nil
}
