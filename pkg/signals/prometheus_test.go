package signals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// promFixture builds the Prometheus API JSON response for a single scalar value.
func promFixture(value string) promResponse {
	return promResponse{
		Status: "success",
		Data: promData{
			ResultType: "vector",
			Result: []promSample{
				{
					Metric: map[string]string{"service": "payment-service"},
					Value:  []interface{}{1609459200.0, value},
				},
			},
		},
	}
}

// writeJSON marshals v to the ResponseWriter as application/json.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// mockPrometheus returns a test server that records every received query and
// dispatches canned responses based on query content. The returned cleanup
// function closes the server.
func mockPrometheus(t *testing.T) (serverURL string, queriesSeen func() []string, cleanup func()) {
	t.Helper()

	var mu sync.Mutex
	var seen []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		q := r.URL.Query().Get("query")

		mu.Lock()
		seen = append(seen, q)
		mu.Unlock()

		switch {
		case strings.Contains(q, "histogram_quantile"):
			// latency: return 0.142 s → collector multiplies to 142.0 ms
			writeJSON(w, promFixture("0.142"))
		case strings.Contains(q, `status=~"5.."`):
			// error rate percentage
			writeJSON(w, promFixture("3.5"))
		case strings.Contains(q, "container_cpu_usage"):
			// CPU utilisation percentage
			writeJSON(w, promFixture("72.0"))
		default:
			// requests per second (rate(http_requests_total{...}[2m]))
			writeJSON(w, promFixture("450.25"))
		}
	}))

	return srv.URL,
		func() []string {
			mu.Lock()
			defer mu.Unlock()
			cp := make([]string, len(seen))
			copy(cp, seen)
			return cp
		},
		srv.Close
}

// ---- happy-path -----------------------------------------------------------

func TestCollect_PopulatesAllFields(t *testing.T) {
	srvURL, queriesSeen, cleanup := mockPrometheus(t)
	defer cleanup()

	c := NewPrometheusCollector(srvURL)
	snap, err := c.Collect(context.Background(), "payment-service")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// All four queries must have been sent.
	qs := queriesSeen()
	if len(qs) != 4 {
		t.Errorf("expected 4 queries, got %d: %v", len(qs), qs)
	}

	// Every query must reference the deployment name.
	for _, q := range qs {
		if !strings.Contains(q, "payment-service") {
			t.Errorf("query missing deployment name: %q", q)
		}
	}

	// Latency: raw value is 0.142 s, collector converts to ms.
	if snap.LatencyP99Ms != 142.0 {
		t.Errorf("LatencyP99Ms: want 142.0, got %v", snap.LatencyP99Ms)
	}
	if snap.ErrorRatePct != 3.5 {
		t.Errorf("ErrorRatePct: want 3.5, got %v", snap.ErrorRatePct)
	}
	if snap.CPUUtilPct != 72.0 {
		t.Errorf("CPUUtilPct: want 72.0, got %v", snap.CPUUtilPct)
	}
	if snap.RequestsPerSec != 450.25 {
		t.Errorf("RequestsPerSec: want 450.25, got %v", snap.RequestsPerSec)
	}
}

func TestCollect_QueriesContainCorrectPromQL(t *testing.T) {
	srvURL, queriesSeen, cleanup := mockPrometheus(t)
	defer cleanup()

	c := NewPrometheusCollector(srvURL)
	if _, err := c.Collect(context.Background(), "checkout-service"); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	qs := queriesSeen()
	assertQueryPresent := func(mustContain ...string) {
		t.Helper()
		for _, needle := range mustContain {
			found := false
			for _, q := range qs {
				if strings.Contains(q, needle) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no query contains %q; queries: %v", needle, qs)
			}
		}
	}

	// Latency query must use histogram_quantile at the 0.99 quantile.
	assertQueryPresent("histogram_quantile(0.99", "http_request_duration_seconds_bucket", "checkout-service")

	// Error-rate query must filter on 5xx status codes.
	assertQueryPresent(`status=~"5.."`, "checkout-service")

	// CPU query must target pods by name prefix.
	assertQueryPresent("container_cpu_usage_seconds_total", `pod=~"checkout-service-.*"`)

	// RPS query must use rate() on http_requests_total.
	assertQueryPresent("rate(http_requests_total{service=\"checkout-service\"}")
}

func TestCollect_TrailingSlashInURL(t *testing.T) {
	srvURL, _, cleanup := mockPrometheus(t)
	defer cleanup()

	// Collector must strip trailing slashes so we don't get double-slash paths.
	c := NewPrometheusCollector(srvURL + "/")
	if _, err := c.Collect(context.Background(), "payment-service"); err != nil {
		t.Fatalf("Collect with trailing slash: %v", err)
	}
}

// ---- empty-result (no data yet) ------------------------------------------

func TestCollect_EmptyResult_ReturnsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, promResponse{
			Status: "success",
			Data:   promData{ResultType: "vector", Result: nil},
		})
	}))
	defer srv.Close()

	c := NewPrometheusCollector(srv.URL)
	snap, err := c.Collect(context.Background(), "new-service")
	if err != nil {
		t.Fatalf("Collect on empty result: %v", err)
	}
	if snap.LatencyP99Ms != 0 || snap.ErrorRatePct != 0 || snap.CPUUtilPct != 0 || snap.RequestsPerSec != 0 {
		t.Errorf("expected all-zero snapshot for empty result, got %+v", snap)
	}
}

// ---- error paths ----------------------------------------------------------

func TestCollect_HTTPError_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewPrometheusCollector(srv.URL)
	_, err := c.Collect(context.Background(), "payment-service")
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500, got: %v", err)
	}
}

func TestCollect_PrometheusErrorStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, promResponse{
			Status:    "error",
			ErrorType: "bad_data",
			Error:     "invalid query",
		})
	}))
	defer srv.Close()

	c := NewPrometheusCollector(srv.URL)
	_, err := c.Collect(context.Background(), "payment-service")
	if err == nil {
		t.Fatal("expected error for prometheus status=error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid query") {
		t.Errorf("error should contain prometheus error message, got: %v", err)
	}
}

func TestCollect_ContextCancelled_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, promFixture("1.0"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	c := NewPrometheusCollector(srv.URL)
	_, err := c.Collect(ctx, "payment-service")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestCollect_UnreachableServer_ReturnsError(t *testing.T) {
	c := NewPrometheusCollector("http://127.0.0.1:19999") // nothing listening here
	_, err := c.Collect(context.Background(), "payment-service")
	if err == nil {
		t.Fatal("expected error for unreachable server, got nil")
	}
}
