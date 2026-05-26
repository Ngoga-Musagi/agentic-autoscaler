/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const collectorHTTPTimeout = 10 * time.Second

// targetToken is replaced with the target Deployment name inside every query
// before it is sent to Prometheus, so a query can be written once and reused.
const targetToken = "$TARGET"

// Default PromQL for standard Prometheus HTTP instrumentation. These reproduce
// the operator's original hard-coded queries; $TARGET is the Deployment name.
const (
	defaultLatencyP99Query = `histogram_quantile(0.99, rate(http_request_duration_seconds_bucket{service="$TARGET"}[2m]))`
	defaultErrorRateQuery  = `rate(http_requests_total{service="$TARGET",status=~"5.."}[2m]) / rate(http_requests_total{service="$TARGET"}[2m]) * 100`
	defaultCPUQuery        = `avg(rate(container_cpu_usage_seconds_total{pod=~"$TARGET-.*"}[2m])) * 100`
	defaultRPSQuery        = `rate(http_requests_total{service="$TARGET"}[2m])`
)

// Queries holds optional PromQL overrides. Any empty field falls back to the
// matching default. This is what makes the operator reusable across services
// that do not follow the default metric-naming conventions.
type Queries struct {
	LatencyP99        string
	ErrorRate         string
	CPUUtilization    string
	RequestsPerSecond string
}

// resolve fills empty fields with defaults and substitutes the target name.
func (q Queries) resolve(target string) Queries {
	sub := func(s, def string) string {
		if strings.TrimSpace(s) == "" {
			s = def
		}
		return strings.ReplaceAll(s, targetToken, target)
	}
	return Queries{
		LatencyP99:        sub(q.LatencyP99, defaultLatencyP99Query),
		ErrorRate:         sub(q.ErrorRate, defaultErrorRateQuery),
		CPUUtilization:    sub(q.CPUUtilization, defaultCPUQuery),
		RequestsPerSecond: sub(q.RequestsPerSecond, defaultRPSQuery),
	}
}

// PrometheusCollector queries the Prometheus HTTP API to collect service metrics.
type PrometheusCollector struct {
	baseURL string
	client  *http.Client
}

// NewPrometheusCollector returns a PrometheusCollector pointed at prometheusURL.
// The collector uses a dedicated HTTP client with a 10-second timeout so that a
// slow Prometheus does not block the reconcile loop indefinitely.
func NewPrometheusCollector(prometheusURL string) *PrometheusCollector {
	return &PrometheusCollector{
		baseURL: strings.TrimRight(prometheusURL, "/"),
		client:  &http.Client{Timeout: collectorHTTPTimeout},
	}
}

// Collect runs the default PromQL queries for targetDeployment. It is a thin
// wrapper over CollectWithQueries with no overrides.
func (c *PrometheusCollector) Collect(ctx context.Context, targetDeployment string) (MetricSnapshot, error) {
	return c.CollectWithQueries(ctx, targetDeployment, Queries{})
}

// CollectWithQueries runs the four signal queries for targetDeployment, using
// any overrides supplied in q and falling back to the defaults otherwise, and
// returns a MetricSnapshot. An empty result from Prometheus (no time-series
// data yet) is treated as zero rather than an error, since new deployments may
// not have metrics.
func (c *PrometheusCollector) CollectWithQueries(ctx context.Context, targetDeployment string, q Queries) (MetricSnapshot, error) {
	rq := q.resolve(targetDeployment)

	latencySec, err := c.query(ctx, rq.LatencyP99)
	if err != nil {
		return MetricSnapshot{}, fmt.Errorf("query latency_p99: %w", err)
	}

	errorRate, err := c.query(ctx, rq.ErrorRate)
	if err != nil {
		return MetricSnapshot{}, fmt.Errorf("query error_rate: %w", err)
	}

	cpuUtil, err := c.query(ctx, rq.CPUUtilization)
	if err != nil {
		return MetricSnapshot{}, fmt.Errorf("query cpu_util: %w", err)
	}

	rps, err := c.query(ctx, rq.RequestsPerSecond)
	if err != nil {
		return MetricSnapshot{}, fmt.Errorf("query requests_per_sec: %w", err)
	}

	return MetricSnapshot{
		LatencyP99Ms:   latencySec * 1000, // Prometheus returns seconds; convert to ms
		ErrorRatePct:   errorRate,
		CPUUtilPct:     cpuUtil,
		RequestsPerSec: rps,
	}, nil
}

// query executes a single instant PromQL query and returns the first scalar result.
// It returns 0 without error when Prometheus has no data for the query (empty vector),
// since that is a valid state for services that have not yet received traffic.
func (c *PrometheusCollector) query(ctx context.Context, promql string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/query", nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	q := req.URL.Query()
	q.Set("query", promql)
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("prometheus returned status %d: %s", resp.StatusCode, body)
	}

	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, fmt.Errorf("parse response: %w", err)
	}
	if pr.Status != "success" {
		return 0, fmt.Errorf("prometheus error (%s): %s", pr.ErrorType, pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return 0, nil
	}

	sample := pr.Data.Result[0]
	// Value is a two-element JSON array: [unix_timestamp_float, "value_string"]
	if len(sample.Value) < 2 {
		return 0, fmt.Errorf("unexpected value format in sample: %v", sample.Value)
	}
	valStr, ok := sample.Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("value[1] is not a string (got %T): %v", sample.Value[1], sample.Value[1])
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse float %q: %w", valStr, err)
	}
	return val, nil
}

// promResponse mirrors the Prometheus HTTP API instant-query response envelope.
type promResponse struct {
	Status    string   `json:"status"`
	Data      promData `json:"data"`
	ErrorType string   `json:"errorType,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type promData struct {
	ResultType string       `json:"resultType"`
	Result     []promSample `json:"result"`
}

// promSample represents one time-series result. Value is [timestamp_float, "value_string"].
type promSample struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`
}
