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
