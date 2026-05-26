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
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Config describes one load run. Normal traffic (rps against TargetURL) works
// against any HTTP service. ErrorPct and DelayMs assume a podinfo-style target
// that exposes /status/{code} and /delay/{seconds} — they shape the operator's
// error-rate and latency signals so scale-up rules fire on demand.
type Config struct {
	TargetURL   string `json:"targetURL"`
	RPS         int    `json:"rps"`
	DurationSec int    `json:"durationSec"`
	ErrorPct    int    `json:"errorPct"`
	DelayMs     int    `json:"delayMs"`
}

// Status is the JSON returned by GET /status.
type Status struct {
	Running      bool   `json:"running"`
	TargetURL    string `json:"targetURL"`
	RPS          int    `json:"rps"`
	RequestsSent int64  `json:"requestsSent"`
	CurrentRPS   int64  `json:"currentRPS"`
	Errors       int64  `json:"errors"`
}

// Driver paces HTTP requests against a target at a configurable rate. A single
// dispatcher ticks at the requested rate and hands work to a bounded worker
// pool, so slow responses consume workers without slowing the dispatch rate.
type Driver struct {
	mu      sync.Mutex
	running bool
	cfg     Config
	cancel  context.CancelFunc

	sent   int64 // atomic
	errs   int64 // atomic
	curRPS int64 // atomic

	client *http.Client
}

// NewDriver returns an idle Driver.
func NewDriver() *Driver {
	return &Driver{client: &http.Client{Timeout: 30 * time.Second}}
}

// Start begins a new load run, replacing any run already in progress. It returns
// an error for invalid configuration but never blocks: the run executes in the
// background until DurationSec elapses or Stop is called.
func (d *Driver) Start(cfg Config) error {
	if cfg.TargetURL == "" {
		return fmt.Errorf("targetURL is required")
	}
	if cfg.RPS < 1 {
		return fmt.Errorf("rps must be at least 1")
	}
	if cfg.ErrorPct < 0 || cfg.ErrorPct > 100 {
		return fmt.Errorf("errorPct must be between 0 and 100")
	}
	if cfg.DurationSec < 1 {
		cfg.DurationSec = 60
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		d.stopLocked()
	}
	atomic.StoreInt64(&d.sent, 0)
	atomic.StoreInt64(&d.errs, 0)
	atomic.StoreInt64(&d.curRPS, 0)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DurationSec)*time.Second)
	d.cfg = cfg
	d.cancel = cancel
	d.running = true
	go d.run(ctx, cfg)
	return nil
}

// Stop halts any running load immediately.
func (d *Driver) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopLocked()
}

func (d *Driver) stopLocked() {
	if d.cancel != nil {
		d.cancel()
	}
	d.running = false
}

// Status returns a snapshot of the current run.
func (d *Driver) Status() Status {
	d.mu.Lock()
	cfg := d.cfg
	running := d.running
	d.mu.Unlock()
	return Status{
		Running:      running,
		TargetURL:    cfg.TargetURL,
		RPS:          cfg.RPS,
		RequestsSent: atomic.LoadInt64(&d.sent),
		CurrentRPS:   atomic.LoadInt64(&d.curRPS),
		Errors:       atomic.LoadInt64(&d.errs),
	}
}

func (d *Driver) run(ctx context.Context, cfg Config) {
	defer func() {
		d.mu.Lock()
		d.running = false
		atomic.StoreInt64(&d.curRPS, 0)
		d.mu.Unlock()
	}()

	workers := cfg.RPS / 2
	if workers < 20 {
		workers = 20
	}
	if workers > 500 {
		workers = 500
	}

	jobs := make(chan string, workers*2)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				d.do(ctx, cfg.TargetURL+path)
			}
		}()
	}

	// Sampler: once a second, publish requests-in-the-last-second as currentRPS.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		prev := atomic.LoadInt64(&d.sent)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				now := atomic.LoadInt64(&d.sent)
				atomic.StoreInt64(&d.curRPS, now-prev)
				prev = now
			}
		}
	}()

	interval := time.Second / time.Duration(cfg.RPS)
	if interval <= 0 {
		interval = time.Microsecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		case <-ticker.C:
			path := pickPath(rng, cfg)
			select {
			case jobs <- path:
			default:
				// Workers saturated (target is slow); skip this tick rather
				// than queue unboundedly.
			}
		}
	}
}

// pickPath chooses the request path for a single request, shaping the signal mix
// according to the configured error and latency injection.
func pickPath(rng *rand.Rand, cfg Config) string {
	roll := rng.Intn(100)
	if cfg.ErrorPct > 0 && roll < cfg.ErrorPct {
		return "/status/500"
	}
	if cfg.DelayMs > 0 && roll%4 == 0 {
		sec := (cfg.DelayMs + 999) / 1000
		if sec < 1 {
			sec = 1
		}
		return "/delay/" + strconv.Itoa(sec)
	}
	return ""
}

func (d *Driver) do(ctx context.Context, url string) {
	atomic.AddInt64(&d.sent, 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		atomic.AddInt64(&d.errs, 1)
		return
	}
	resp, err := d.client.Do(req)
	if err != nil {
		atomic.AddInt64(&d.errs, 1)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		atomic.AddInt64(&d.errs, 1)
	}
}
