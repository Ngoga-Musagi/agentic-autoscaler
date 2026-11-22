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

// Command agentrunner is the AI-agent sample workload for the multi-service
// demo. DEMO / TESTING ONLY — never run it in production. One binary, two roles:
//
//   - ROLE=runner (default): an agent worker. Each task (GET / or POST /task)
//     waits for one of WORKERS slots, calls the LLM (retrying 429s), then calls a
//     tool. It is I/O-bound by construction: under overload its CPU stays near
//     idle while the backlog and task latency climb — the blind spot of a CPU HPA.
//   - ROLE=llm: a mock LLM provider with a SHARED requests-per-minute quota,
//     latency and a token budget. One replica stands in for the real provider,
//     so adding runner pods adds calls against the same quota — exactly why the
//     operator must hold, not scale, when the provider is rate-limiting.
//
// Both roles expose Prometheus metrics on /metrics and a fault control API on
// /fault (see runner.go and llm.go). No paid API key is involved.
package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func main() {
	reg := prometheus.NewRegistry()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	role := envOr("ROLE", "runner")
	switch role {
	case "llm":
		l := newMockLLM(reg, llmConfig{
			rpm:          envInt("LLM_RPM", 6000),
			latency:      time.Duration(envInt("LLM_LATENCY_MS", 300)) * time.Millisecond,
			budgetPerMin: envInt("TOKEN_BUDGET_PER_MIN", 0),
		})
		mux.HandleFunc("/v1/complete", l.handleComplete)
		mux.HandleFunc("/fault", l.handleFault)
	case "runner":
		r := newRunner(reg, runnerConfig{
			service:     envOr("SERVICE_NAME", "agent-runner"),
			workers:     envInt("WORKERS", 4),
			maxQueue:    envInt("MAX_QUEUE", 200),
			llmURL:      envOr("LLM_URL", "http://mock-llm:8080"),
			llmAttempts: envInt("LLM_MAX_ATTEMPTS", 3),
			toolLatency: time.Duration(envInt("TOOL_LATENCY_MS", 200)) * time.Millisecond,
		}, faultsFromEnv())
		mux.HandleFunc("/fault", r.handleFault)
		mux.HandleFunc("/task", r.handleTask)
		mux.HandleFunc("/", r.handleTask) // loadgen drives GET /
	default:
		log.Fatalf("unknown ROLE %q (want runner or llm)", role)
	}

	addr := envOr("LISTEN_ADDR", ":8080")
	log.Printf("agentrunner role=%s listening on %s", role, addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
