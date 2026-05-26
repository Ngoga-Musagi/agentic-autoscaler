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

package main

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartValidation(t *testing.T) {
	d := NewDriver()
	if err := d.Start(Config{RPS: 10}); err == nil {
		t.Error("expected error for missing targetURL")
	}
	if err := d.Start(Config{TargetURL: "http://x", RPS: 0}); err == nil {
		t.Error("expected error for rps < 1")
	}
	if err := d.Start(Config{TargetURL: "http://x", RPS: 10, ErrorPct: 150}); err == nil {
		t.Error("expected error for errorPct > 100")
	}
}

func TestPickPathRouting(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var root, errs, delays int
	cfg := Config{ErrorPct: 30, DelayMs: 1000}
	for i := 0; i < 10000; i++ {
		switch p := pickPath(rng, cfg); {
		case p == "":
			root++
		case strings.HasPrefix(p, "/status/5"):
			errs++
		case strings.HasPrefix(p, "/delay/"):
			delays++
		default:
			t.Fatalf("unexpected path %q", p)
		}
	}
	// ~30% errors expected; allow a wide band to stay non-flaky.
	if errs < 2000 || errs > 4000 {
		t.Errorf("error routing off: got %d/10000", errs)
	}
	if delays == 0 {
		t.Errorf("expected some delayed requests")
	}
	if root == 0 {
		t.Errorf("expected some root requests")
	}
}

func TestPickPathNoInjection(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		if p := pickPath(rng, Config{}); p != "" {
			t.Fatalf("expected only root path with no injection, got %q", p)
		}
	}
}

func TestDriverSendsAndStops(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDriver()
	if err := d.Start(Config{TargetURL: srv.URL, RPS: 100, DurationSec: 5}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)

	if !d.Status().Running {
		t.Error("driver should be running")
	}
	d.Stop()
	time.Sleep(100 * time.Millisecond)

	sent := atomic.LoadInt64(&hits)
	if sent == 0 {
		t.Error("expected the driver to send requests")
	}
	if d.Status().Running {
		t.Error("driver should have stopped")
	}

	// After Stop, the hit count must not keep climbing.
	before := atomic.LoadInt64(&hits)
	time.Sleep(300 * time.Millisecond)
	if after := atomic.LoadInt64(&hits); after > before+5 {
		t.Errorf("driver kept sending after stop: %d -> %d", before, after)
	}
}
