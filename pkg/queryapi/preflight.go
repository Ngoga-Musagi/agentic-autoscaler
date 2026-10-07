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

package queryapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// preflightTimeout bounds each reachability check so a wrong URL fails fast
// rather than hanging the Onboard flow.
const preflightTimeout = 8 * time.Second

// preflightRequest is the POST /api/preflight body: the signal endpoints a user
// is about to put in an AgenticAutoscaler CR, to validate before creating it.
type preflightRequest struct {
	PrometheusURL string `json:"prometheusURL"`
	PromQuery     string `json:"promQuery"`
	LokiURL       string `json:"lokiURL"`
	LokiQuery     string `json:"lokiQuery"`
}

// checkResult is the outcome of one endpoint check.
type checkResult struct {
	Checked bool   `json:"checked"` // false when no URL was provided
	OK      bool   `json:"ok"`      // reachable and well-formed response
	Results int    `json:"results"` // number of series/streams returned
	Detail  string `json:"detail"`  // human-readable status or error
}

// preflightResponse reports per-signal reachability so the Onboard UI can tell
// the user "your Prometheus/Loki are reachable and the query returns data"
// BEFORE the CR is created — the #1 cause of "it didn't scale, why?".
type preflightResponse struct {
	Prometheus checkResult `json:"prometheus"`
	Loki       checkResult `json:"loki"`
}

// handlePreflight validates user-supplied Prometheus/Loki endpoints. It is
// write-gated: it makes outbound requests to caller-supplied URLs, so it shares
// the console's write-mode + token gate rather than being open to any reader.
func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireWrite(w, r) {
		return
	}
	var req preflightRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	client := &http.Client{Timeout: preflightTimeout}
	resp := preflightResponse{
		Prometheus: checkPrometheus(r.Context(), client, req.PrometheusURL, req.PromQuery),
		Loki:       checkLoki(r.Context(), client, req.LokiURL, req.LokiQuery),
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkPrometheus runs an instant query against the given Prometheus and reports
// whether it is reachable and the query returned any series. An empty promQuery
// defaults to "up" (a reachability probe).
func checkPrometheus(ctx context.Context, c *http.Client, promURL, promQuery string) checkResult {
	promURL = strings.TrimSpace(promURL)
	if promURL == "" {
		return checkResult{Checked: false, Detail: "no Prometheus URL provided"}
	}
	query := strings.TrimSpace(promQuery)
	if query == "" {
		query = "up"
	}
	endpoint := strings.TrimRight(promURL, "/") + "/api/v1/query?query=" + url.QueryEscape(query)

	count, err := queryResultCount(ctx, c, endpoint)
	if err != nil {
		return checkResult{Checked: true, OK: false, Detail: "unreachable or bad response: " + err.Error()}
	}
	if count == 0 {
		return checkResult{Checked: true, OK: true, Results: 0,
			Detail: "reachable, but the query returned no series — check the metric name / labels"}
	}
	return checkResult{Checked: true, OK: true, Results: count,
		Detail: "reachable; query returned " + strconv.Itoa(count) + " series"}
}

// checkLoki runs a short range query against the given Loki and reports whether
// it is reachable and returned any log streams.
func checkLoki(ctx context.Context, c *http.Client, lokiURL, lokiQuery string) checkResult {
	lokiURL = strings.TrimSpace(lokiURL)
	if lokiURL == "" {
		return checkResult{Checked: false, Detail: "no Loki URL provided"}
	}
	query := strings.TrimSpace(lokiQuery)
	if query == "" {
		return checkResult{Checked: true, OK: false, Detail: "a LogQL query is required to test Loki"}
	}
	now := time.Now()
	v := url.Values{}
	v.Set("query", query)
	v.Set("limit", "1")
	v.Set("start", strconv.FormatInt(now.Add(-5*time.Minute).UnixNano(), 10))
	v.Set("end", strconv.FormatInt(now.UnixNano(), 10))
	endpoint := strings.TrimRight(lokiURL, "/") + "/loki/api/v1/query_range?" + v.Encode()

	count, err := queryResultCount(ctx, c, endpoint)
	if err != nil {
		return checkResult{Checked: true, OK: false, Detail: "unreachable or bad response: " + err.Error()}
	}
	if count == 0 {
		return checkResult{Checked: true, OK: true, Results: 0,
			Detail: "reachable, but no log streams matched in the last 5m — check the LogQL selector"}
	}
	return checkResult{Checked: true, OK: true, Results: count,
		Detail: "reachable; matched " + strconv.Itoa(count) + " stream(s)"}
}

// queryResultCount GETs a Prometheus/Loki query URL (both share the
// {status,data:{result:[...]}} envelope) and returns len(data.result).
func queryResultCount(ctx context.Context, c *http.Client, endpoint string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var env struct {
		Status string `json:"status"`
		Data   struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return 0, fmt.Errorf("unexpected response shape")
	}
	if env.Status != "success" {
		return 0, fmt.Errorf("query status %q", env.Status)
	}
	return len(env.Data.Result), nil
}
