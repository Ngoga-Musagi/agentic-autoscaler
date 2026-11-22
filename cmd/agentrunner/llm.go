package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// tokensPerCompletion is what every mock completion "costs" against the budget.
const tokensPerCompletion = 800

// llmConfig is the mock provider's quota and behaviour.
type llmConfig struct {
	rpm          int           // shared requests-per-minute quota (all runner pods)
	latency      time.Duration // per-completion latency
	budgetPerMin int           // token budget per minute; 0 disables the budget gauge
}

// mockLLM is a single-replica stand-in for an LLM provider. Its rate limit is a
// token bucket shared by every caller, like an API key's quota.
type mockLLM struct {
	mu       sync.Mutex
	cfg      llmConfig
	bucket   float64
	refilled time.Time
	used     [60]int // tokens served per second over the last minute (ring)
	usedSec  int64   // unix second the ring head belongs to

	requests *prometheus.CounterVec
}

func newMockLLM(reg prometheus.Registerer, cfg llmConfig) *mockLLM {
	l := &mockLLM{cfg: cfg, refilled: time.Now(), bucket: burst(cfg.rpm)}
	l.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mockllm_requests_total",
		Help: "Mock LLM completions by HTTP status.",
	}, []string{"code"})
	budget := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "mockllm_token_budget_used_percent",
		Help: "Tokens served in the last minute as a percent of the per-minute budget.",
	}, l.budgetUsedPercent)
	reg.MustRegister(l.requests, budget)
	return l
}

// burst allows one second's worth of quota at once.
func burst(rpm int) float64 {
	if b := float64(rpm) / 60; b > 1 {
		return b
	}
	return 1
}

// allow takes one request from the shared bucket.
func (l *mockLLM) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bucket += now.Sub(l.refilled).Seconds() * float64(l.cfg.rpm) / 60
	if b := burst(l.cfg.rpm); l.bucket > b {
		l.bucket = b
	}
	l.refilled = now
	if l.bucket < 1 {
		return false
	}
	l.bucket--
	return true
}

// spend records tokens in the per-second ring, clearing seconds that elapsed.
func (l *mockLLM) spend(now time.Time, tokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.advance(now.Unix())
	l.used[now.Unix()%60] += tokens
}

func (l *mockLLM) advance(sec int64) {
	if l.usedSec == 0 || sec-l.usedSec >= 60 {
		l.used = [60]int{}
	} else {
		for s := l.usedSec + 1; s <= sec; s++ {
			l.used[s%60] = 0
		}
	}
	l.usedSec = sec
}

func (l *mockLLM) budgetUsedPercent() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cfg.budgetPerMin <= 0 {
		return 0
	}
	l.advance(time.Now().Unix())
	total := 0
	for _, t := range l.used {
		total += t
	}
	return 100 * float64(total) / float64(l.cfg.budgetPerMin)
}

func (l *mockLLM) handleComplete(w http.ResponseWriter, r *http.Request) {
	if !l.allow(time.Now()) {
		l.requests.WithLabelValues("429").Inc()
		w.Header().Set("Retry-After", "1")
		http.Error(w, `{"error":"rate_limit_exceeded"}`, http.StatusTooManyRequests)
		return
	}
	l.mu.Lock()
	latency := l.cfg.latency
	l.mu.Unlock()
	select {
	case <-time.After(latency):
	case <-r.Context().Done():
		return
	}
	l.spend(time.Now(), tokensPerCompletion)
	l.requests.WithLabelValues(strconv.Itoa(http.StatusOK)).Inc()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"tokens": tokensPerCompletion})
}

// llmFault is the POST /fault body; zero fields leave the setting unchanged.
type llmFault struct {
	RPM               int `json:"rpm"`
	LatencyMs         int `json:"latencyMs"`
	TokenBudgetPerMin int `json:"tokenBudgetPerMin"`
}

func (l *mockLLM) handleFault(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var f llmFault
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		l.mu.Lock()
		if f.RPM > 0 {
			l.cfg.rpm = f.RPM
		}
		if f.LatencyMs > 0 {
			l.cfg.latency = time.Duration(f.LatencyMs) * time.Millisecond
		}
		if f.TokenBudgetPerMin > 0 {
			l.cfg.budgetPerMin = f.TokenBudgetPerMin
		}
		log.Printf("mock LLM set: rpm=%d latency=%s tokenBudgetPerMin=%d", l.cfg.rpm, l.cfg.latency, l.cfg.budgetPerMin)
		l.mu.Unlock()
	}
	l.mu.Lock()
	out := llmFault{RPM: l.cfg.rpm, LatencyMs: int(l.cfg.latency.Milliseconds()), TokenBudgetPerMin: l.cfg.budgetPerMin}
	l.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
