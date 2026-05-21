// Command mockserver is a throwaway test double for Prometheus + Loki used by
// the local "run the whole application" demo. It serves the two HTTP endpoints
// the operator's signal collector calls and returns canned data that triggers
// the rule-based agent's "connection-pool-exhausted + latency spike" scale-up.
//
// Run: go run ./hack/mockserver   (listens on :9099)
package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func main() {
	mux := http.NewServeMux()

	// Prometheus instant-query API. The collector sends four queries; we route
	// on the PromQL text and return values that paint an "overloaded" picture.
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		value := "0"
		switch {
		case strings.Contains(q, "histogram_quantile"):
			value = "2.5" // p99 latency in SECONDS → 2500 ms (> 2000 ms rule threshold)
		case strings.Contains(q, "status"):
			value = "3" // HTTP 5xx error rate %
		case strings.Contains(q, "container_cpu"):
			value = "40" // CPU utilisation %
		default:
			value = "120" // requests/sec
		}
		log.Printf("PROM query=%q -> %s", truncate(q, 60), value)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w,
			`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[%d,%q]}]}}`,
			time.Now().Unix(), value)
	})

	// Loki query_range API — return one log line that matches the
	// connection-pool-exhausted pattern (regex: "connection pool exhausted").
	mux.HandleFunc("/loki/api/v1/query_range", func(w http.ResponseWriter, r *http.Request) {
		ts := strconv.FormatInt(time.Now().UnixNano(), 10)
		line := "ERROR connection pool exhausted: all 50 db connections in use"
		log.Printf("LOKI query=%q -> 1 log line", truncate(r.URL.Query().Get("query"), 40))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w,
			`{"status":"success","data":{"resultType":"streams","result":[{"stream":{"app":"payment-service","level":"error"},"values":[[%q,%q]]}]}}`,
			ts, line)
	})

	addr := ":9099"
	log.Printf("mock Prometheus+Loki listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
