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

// Command faultgen is a fault-injecting HTTP echo service for the timeout-storm
// demo. Driven by the load generator, it serves podinfo-compatible paths and
// exposes Prometheus metrics (labelled with a `service` matching the operator's
// $TARGET queries) so the operator reads real latency and error signals. When a
// fault scenario is switched on via the control API it does two things at once:
//
//  1. adds high latency to responses, so p99 crosses rule 2's 2000 ms threshold, and
//  2. writes matching log lines to stdout, ingested by Promtail → Loki → fusion.
//
// Together these make the operator's rule 2 (connection-pool-exhausted AND high
// p99 latency) fire from genuine signals, not from anything faked inside the
// operator. It is a demo/testing tool — never run it in production.
//
// HTTP API (default :8080):
//
//	GET  /                        → 200 echo; slow while a fault is active
//	GET  /status/{code}           → returns {code} (podinfo-compatible error injection)
//	GET  /delay/{seconds}         → sleeps then 200 (podinfo-compatible latency)
//	GET  /healthz                 → 200 ok
//	GET  /metrics                 → Prometheus metrics
//	POST /fault {enabled,scenario,latencyMs} → toggle a fault scenario
//	GET  /fault                   → current fault state JSON
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// stormLatency is the response latency injected while a fault is active. It sits
// above rule 2's 2000 ms p99 threshold so the metric side of the rule is met.
const stormLatency = 2500 * time.Millisecond

// emitInterval is how often the fault line is written to stdout while a fault is
// active — steady enough for Loki to always have a fresh match in its window,
// independent of request traffic.
const emitInterval = 400 * time.Millisecond

// serviceName is the value of the `service` metric label, matching the operator
// $TARGET substitution. Set SERVICE_NAME to the target Deployment's name.
func serviceName() string {
	if v := os.Getenv("SERVICE_NAME"); v != "" {
		return v
	}
	return "faultgen"
}

// metrics holds the two series the operator's default PromQL reads.
type metrics struct {
	duration prometheus.Histogram
	requests *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer, service string) *metrics {
	m := &metrics{
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:        "http_request_duration_seconds",
			Help:        "HTTP request latency in seconds.",
			ConstLabels: prometheus.Labels{"service": service},
			// Buckets span the 2000 ms rule-2 threshold so p99 resolves above it
			// during a storm (2.5 s lands between the 2 s and 3 s buckets).
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5, 10},
		}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "http_requests_total",
			Help:        "HTTP requests by response status.",
			ConstLabels: prometheus.Labels{"service": service},
		}, []string{"status"}),
	}
	reg.MustRegister(m.duration, m.requests)
	return m
}

// observe records one request's latency and status code.
func (m *metrics) observe(status int, d time.Duration) {
	m.duration.Observe(d.Seconds())
	m.requests.WithLabelValues(strconv.Itoa(status)).Inc()
}

// faultState is the toggle set by the control API and read by the request path
// and the background emitter.
type faultState struct {
	mu       sync.RWMutex
	enabled  bool
	scenario string
	latency  time.Duration
}

func (f *faultState) snapshot() (bool, string, time.Duration) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.enabled, f.scenario, f.latency
}

func (f *faultState) set(enabled bool, scenario string, latency time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = enabled
	f.scenario = scenario
	f.latency = latency
}

func main() {
	addr := ":8080"
	if p := os.Getenv("FAULTGEN_ADDR"); p != "" {
		addr = p
	}
	service := serviceName()

	reg := prometheus.NewRegistry()
	m := newMetrics(reg, service)

	// Initial fault state from the environment, so every replica — including
	// ones the operator scales up during a storm — faults from boot without a
	// per-pod API call. FAULT_ENABLED=true starts the storm; FAULT_SCENARIO
	// selects the pattern (default connection-pool-exhausted).
	scenario := os.Getenv("FAULT_SCENARIO")
	if scenario == "" {
		scenario = defaultScenario
	}
	fault := &faultState{
		enabled:  os.Getenv("FAULT_ENABLED") == "true",
		scenario: scenario,
		latency:  stormLatency,
	}
	if fault.enabled {
		log.Printf("fault enabled at boot: scenario=%q latency=%s", scenario, stormLatency)
	}

	// Background emitter: while a fault is active, write the scenario's log line
	// to stdout on a steady cadence so Loki always has a fresh match.
	go func() {
		t := time.NewTicker(emitInterval)
		defer t.Stop()
		for range t.C {
			if enabled, scenario, _ := fault.snapshot(); enabled {
				log.Println(logLineFor(scenario))
			}
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/fault", func(w http.ResponseWriter, r *http.Request) {
		handleFault(w, r, fault, service)
	})

	// Echo + podinfo-compatible paths, all instrumented.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		status := serve(w, r, fault)
		m.observe(status, time.Since(start))
	})

	log.Printf("faultgen listening on %s (service=%q)", addr, service)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// serve handles echo and podinfo-compatible paths and returns the status code
// it wrote, so the caller can record it. While a fault is active, the echo path
// sleeps the injected latency so the recorded duration (and thus p99) climbs.
func serve(w http.ResponseWriter, r *http.Request, fault *faultState) int {
	path := r.URL.Path

	switch {
	case strings.HasPrefix(path, "/status/"):
		code, err := strconv.Atoi(strings.TrimPrefix(path, "/status/"))
		if err != nil || code < 100 || code > 599 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		return code

	case strings.HasPrefix(path, "/delay/"):
		sec, err := strconv.Atoi(strings.TrimPrefix(path, "/delay/"))
		if err == nil && sec > 0 {
			if sec > 30 {
				sec = 30
			}
			time.Sleep(time.Duration(sec) * time.Second)
		}
		w.WriteHeader(http.StatusOK)
		return http.StatusOK

	default:
		if enabled, _, latency := fault.snapshot(); enabled && latency > 0 {
			time.Sleep(latency)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return http.StatusOK
	}
}

// faultRequest is the POST /fault body.
type faultRequest struct {
	Enabled   bool   `json:"enabled"`
	Scenario  string `json:"scenario"`
	LatencyMs int    `json:"latencyMs"`
}

// faultResponse is the GET/POST /fault reply.
type faultResponse struct {
	Enabled   bool   `json:"enabled"`
	Scenario  string `json:"scenario"`
	LatencyMs int    `json:"latencyMs"`
	Service   string `json:"service"`
	LogLine   string `json:"logLine"`
}

func handleFault(w http.ResponseWriter, r *http.Request, fault *faultState, service string) {
	switch r.Method {
	case http.MethodGet:
		enabled, scenario, latency := fault.snapshot()
		writeFaultState(w, enabled, scenario, latency, service)

	case http.MethodPost:
		var req faultRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		scenario := req.Scenario
		if scenario == "" {
			scenario = defaultScenario
		}
		latency := stormLatency
		if req.LatencyMs > 0 {
			latency = time.Duration(req.LatencyMs) * time.Millisecond
		}
		fault.set(req.Enabled, scenario, latency)
		log.Printf("fault set: enabled=%v scenario=%q latency=%s", req.Enabled, scenario, latency)
		writeFaultState(w, req.Enabled, scenario, latency, service)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeFaultState(w http.ResponseWriter, enabled bool, scenario string, latency time.Duration, service string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(faultResponse{
		Enabled:   enabled,
		Scenario:  scenario,
		LatencyMs: int(latency.Milliseconds()),
		Service:   service,
		LogLine:   logLineFor(scenario),
	})
}
