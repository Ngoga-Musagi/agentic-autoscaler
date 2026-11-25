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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueriesResolveDefaults(t *testing.T) {
	got := Queries{}.resolve("payment-service")
	if strings.Contains(got.LatencyP99+got.ErrorRate+got.CPUUtilization+got.RequestsPerSecond, targetToken) {
		t.Errorf("$TARGET not substituted: %+v", got)
	}
	if !strings.Contains(got.RequestsPerSecond, `http_requests_total{service="payment-service"}`) {
		t.Errorf("default RPS query wrong: %s", got.RequestsPerSecond)
	}
	if !strings.Contains(got.CPUUtilization, `pod=~"payment-service-.*"`) {
		t.Errorf("default CPU query wrong: %s", got.CPUUtilization)
	}
}

func TestQueriesResolveOverride(t *testing.T) {
	q := Queries{RequestsPerSecond: `rate(custom_requests_total{app="$TARGET"}[1m])`}
	got := q.resolve("checkout")

	if want := `rate(custom_requests_total{app="checkout"}[1m])`; got.RequestsPerSecond != want {
		t.Errorf("override not applied: got %q want %q", got.RequestsPerSecond, want)
	}
	// Unspecified fields still fall back to the defaults.
	if !strings.Contains(got.CPUUtilization, "container_cpu_usage_seconds_total") {
		t.Errorf("unspecified field lost its default: %s", got.CPUUtilization)
	}
}

func TestCollectWithQueriesSendsOverride(t *testing.T) {
	var captured []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = append(captured, r.URL.Query().Get("query"))
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"5"]}]}}`)
	}))
	defer srv.Close()

	c := NewPrometheusCollector(srv.URL)
	_, err := c.CollectWithQueries(context.Background(), "checkout",
		Queries{RequestsPerSecond: `myrps{app="$TARGET"}`})
	if err != nil {
		t.Fatal(err)
	}

	var sawOverride bool
	for _, q := range captured {
		if strings.Contains(q, targetToken) {
			t.Errorf("query reached Prometheus with unsubstituted token: %s", q)
		}
		if q == `myrps{app="checkout"}` {
			sawOverride = true
		}
	}
	if !sawOverride {
		t.Errorf("custom RPS query was not sent; captured=%v", captured)
	}
}

// A service with its own metric names (the multi-service demo's checkout) must
// produce a correct snapshot purely from spec.metrics overrides: every query
// reaches Prometheus with the target substituted, and each result lands in the
// right field (latency converted from seconds to ms).
func TestCollectWithQueries_NonDefaultNamesProduceSnapshot(t *testing.T) {
	const (
		latQ = `histogram_quantile(0.99, sum by (le) (rate(shop_request_seconds_bucket{app="$TARGET"}[2m]))) >= 0`
		errQ = `sum(rate(shop_requests_total{app="$TARGET",code=~"5.."}[2m])) / ` +
			`(sum(rate(shop_requests_total{app="$TARGET"}[2m])) > 0) * 100`
		cpuQ = `avg(rate(container_cpu_usage_seconds_total{pod=~"$TARGET-.*"}[2m])) * 100`
	)
	expand := func(q string) string { return strings.ReplaceAll(q, "$TARGET", "checkout") }
	answers := map[string]string{
		expand(latQ): "2.75",
		expand(errQ): "4",
		expand(cpuQ): "3.5",
		`sum(rate(shop_requests_total{app="checkout"}[2m]))`: "120",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := answers[r.URL.Query().Get("query")]
		if !ok {
			t.Errorf("unexpected query: %s", r.URL.Query().Get("query"))
			v = "0"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector",`+
			`"result":[{"metric":{},"value":[1,%q]}]}}`, v)
	}))
	defer srv.Close()

	got, err := NewPrometheusCollector(srv.URL).CollectWithQueries(context.Background(), "checkout", Queries{
		LatencyP99:        latQ,
		ErrorRate:         errQ,
		CPUUtilization:    cpuQ,
		RequestsPerSecond: `sum(rate(shop_requests_total{app="$TARGET"}[2m]))`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := MetricSnapshot{LatencyP99Ms: 2750, ErrorRatePct: 4, CPUUtilPct: 3.5, RequestsPerSec: 120}
	if got.LatencyP99Ms != want.LatencyP99Ms || got.ErrorRatePct != want.ErrorRatePct ||
		got.CPUUtilPct != want.CPUUtilPct || got.RequestsPerSec != want.RequestsPerSec {
		t.Errorf("snapshot: want %+v, got %+v", want, got)
	}
}

func TestPromQLBacklog_SubstitutesTargetAndTreatsNoDataAsZero(t *testing.T) {
	var got []string
	empty := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query().Get("query"))
		if empty {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"180"]}]}}`)
	}))
	defer srv.Close()

	var src BacklogSource = NewPromQLBacklog(NewPrometheusCollector(srv.URL), "agent-runner",
		`sum(agent_tasks_queued{app="$TARGET"})`)
	v, err := src.Backlog(context.Background())
	if err != nil || v != 180 {
		t.Fatalf("backlog: want 180, got %v (err %v)", v, err)
	}
	if got[0] != `sum(agent_tasks_queued{app="agent-runner"})` {
		t.Errorf("query not substituted: %s", got[0])
	}
	empty = true
	if v, err := src.Backlog(context.Background()); err != nil || v != 0 {
		t.Errorf("no data: want 0, got %v (err %v)", v, err)
	}
}

func TestPromQLBacklog_PropagatesPrometheusErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := NewPromQLBacklog(NewPrometheusCollector(srv.URL), "x", "q").Backlog(context.Background()); err == nil {
		t.Fatal("want an error so the caller can skip the signal")
	}
}
