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

// Command loadgen is a small HTTP load generator with a control API. It exists
// to demonstrate the agentic autoscaler reacting to real traffic: drive load at
// a service, watch the operator scale it up, then stop and watch it scale back
// down. It is a demo/testing tool — not for production.
//
// Control API (default :8080):
//
//	POST /start   {targetURL, rps, durationSec, errorPct, delayMs}
//	POST /stop
//	GET  /status  → Status JSON
//	GET  /healthz → 200 ok
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func main() {
	addr := ":8080"
	if p := os.Getenv("LOADGEN_ADDR"); p != "" {
		addr = p
	}

	driver := NewDriver()
	mux := http.NewServeMux()

	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var cfg Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := driver.Start(cfg); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		log.Printf("load started: target=%s rps=%d duration=%ds errorPct=%d delayMs=%d",
			cfg.TargetURL, cfg.RPS, cfg.DurationSec, cfg.ErrorPct, cfg.DelayMs)
		writeJSON(w, http.StatusOK, driver.Status())
	})

	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		driver.Stop()
		log.Printf("load stopped")
		writeJSON(w, http.StatusOK, driver.Status())
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, driver.Status())
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("loadgen control API listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("loadgen server stopped: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
