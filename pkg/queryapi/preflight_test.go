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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockQueryServer returns an httptest server that answers the Prometheus/Loki
// query envelope with `results` entries (or an HTTP error when status != 200).
func mockQueryServer(t *testing.T, results int, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		res := make([]json.RawMessage, results)
		for i := range res {
			res[i] = json.RawMessage(`{}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"data":   map[string]interface{}{"result": res},
		})
	}))
}

func postPreflight(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/preflight", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handlePreflight(rec, req)
	return rec
}

func TestPreflight_WriteDisabled_Returns403(t *testing.T) {
	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: false})
	rec := postPreflight(t, s, `{"prometheusURL":"http://x"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 when write disabled, got %d", rec.Code)
	}
}

func TestPreflight_PrometheusReachable_ReportsResults(t *testing.T) {
	prom := mockQueryServer(t, 3, http.StatusOK)
	defer prom.Close()

	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true})
	rec := postPreflight(t, s, `{"prometheusURL":"`+prom.URL+`","promQuery":"up"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp preflightResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Prometheus.Checked || !resp.Prometheus.OK || resp.Prometheus.Results != 3 {
		t.Errorf("prometheus check: want checked+ok+3 results, got %+v", resp.Prometheus)
	}
	// No Loki URL supplied → not checked.
	if resp.Loki.Checked {
		t.Errorf("loki should be unchecked when no URL given, got %+v", resp.Loki)
	}
}

func TestPreflight_PrometheusUnreachable_OKFalse(t *testing.T) {
	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true})
	// Port 1 is not listening → connection refused.
	rec := postPreflight(t, s, `{"prometheusURL":"http://127.0.0.1:1","promQuery":"up"}`)
	var resp preflightResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Prometheus.Checked || resp.Prometheus.OK {
		t.Errorf("unreachable prometheus should be checked but not ok, got %+v", resp.Prometheus)
	}
}

func TestPreflight_PrometheusHTTPError_OKFalse(t *testing.T) {
	// A reachable Prometheus that returns a non-200 (e.g. 500) is not ok.
	prom := mockQueryServer(t, 0, http.StatusInternalServerError)
	defer prom.Close()

	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true})
	rec := postPreflight(t, s, `{"prometheusURL":"`+prom.URL+`","promQuery":"up"}`)
	var resp preflightResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Prometheus.Checked || resp.Prometheus.OK {
		t.Errorf("HTTP 500 from prometheus must be checked but not ok, got %+v", resp.Prometheus)
	}
	if !strings.Contains(resp.Prometheus.Detail, "500") {
		t.Errorf("detail should mention the HTTP status, got %q", resp.Prometheus.Detail)
	}
}

func TestPreflight_PrometheusReachableNoData_OKButZero(t *testing.T) {
	prom := mockQueryServer(t, 0, http.StatusOK)
	defer prom.Close()

	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true})
	rec := postPreflight(t, s, `{"prometheusURL":"`+prom.URL+`","promQuery":"up"}`)
	var resp preflightResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Prometheus.OK || resp.Prometheus.Results != 0 {
		t.Errorf("reachable-but-empty should be ok with 0 results, got %+v", resp.Prometheus)
	}
	if !strings.Contains(resp.Prometheus.Detail, "no series") {
		t.Errorf("detail should flag empty result, got %q", resp.Prometheus.Detail)
	}
}

func TestPreflight_Loki_ReachableWithStreams(t *testing.T) {
	loki := mockQueryServer(t, 2, http.StatusOK)
	defer loki.Close()

	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true})
	rec := postPreflight(t, s, `{"lokiURL":"`+loki.URL+`","lokiQuery":"{app=\"x\"}"}`)
	var resp preflightResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Loki.Checked || !resp.Loki.OK || resp.Loki.Results != 2 {
		t.Errorf("loki check: want checked+ok+2, got %+v", resp.Loki)
	}
}

func TestPreflight_Loki_NoQuery_NotOK(t *testing.T) {
	loki := mockQueryServer(t, 1, http.StatusOK)
	defer loki.Close()

	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true})
	rec := postPreflight(t, s, `{"lokiURL":"`+loki.URL+`"}`)
	var resp preflightResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Loki.OK {
		t.Errorf("loki with no query must not be ok, got %+v", resp.Loki)
	}
}
