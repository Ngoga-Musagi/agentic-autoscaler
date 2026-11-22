package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// toolTimeout is how long a tool call hangs before giving up while the
// tool-timeout fault is active.
const toolTimeout = 2 * time.Second

// runnerConfig is fixed for the pod's lifetime.
type runnerConfig struct {
	service     string
	workers     int // concurrent tasks per pod — the capacity that scaling adds
	maxQueue    int // tasks allowed to wait for a worker before rejecting
	llmURL      string
	llmAttempts int
	toolLatency time.Duration
}

// runnerFaults are the injectable agentic failure modes (percent of tasks).
// They start from the environment so pods scaled up mid-fault inherit them.
type runnerFaults struct {
	mu                 sync.RWMutex
	ToolTimeoutPct     int `json:"toolTimeoutPct"`
	ContextOverflowPct int `json:"contextOverflowPct"`
}

func faultsFromEnv() *runnerFaults {
	return &runnerFaults{
		ToolTimeoutPct:     envInt("TOOL_TIMEOUT_PCT", 0),
		ContextOverflowPct: envInt("CONTEXT_OVERFLOW_PCT", 0),
	}
}

func (f *runnerFaults) get() (toolTimeoutPct, contextOverflowPct int) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.ToolTimeoutPct, f.ContextOverflowPct
}

// runnerMetrics are the agentic signals the agent-runner CRs read, labelled
// app=<service>, plus the standard http_* series so the default queries work.
type runnerMetrics struct {
	taskSeconds  prometheus.Histogram
	tasks        *prometheus.CounterVec
	llmCalls     *prometheus.CounterVec
	llmTokens    prometheus.Counter
	toolTimeouts prometheus.Counter
	httpDuration prometheus.Histogram
	httpRequests *prometheus.CounterVec
}

type runner struct {
	cfg     runnerConfig
	faults  *runnerFaults
	slots   chan struct{}
	queued  atomic.Int64
	running atomic.Int64
	m       runnerMetrics
	client  *http.Client
	logs    *throttle
}

func newRunner(reg prometheus.Registerer, cfg runnerConfig, faults *runnerFaults) *runner {
	r := &runner{
		cfg:    cfg,
		faults: faults,
		slots:  make(chan struct{}, cfg.workers),
		client: &http.Client{Timeout: 30 * time.Second},
		logs:   newThrottle(500 * time.Millisecond),
	}
	app := prometheus.Labels{"app": cfg.service}
	// Buckets reach well past the 2 s rule-2 threshold: queued tasks wait tens of seconds.
	buckets := []float64{0.25, 0.5, 1, 2, 3, 5, 10, 20, 30, 60}
	r.m = runnerMetrics{
		taskSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "agent_task_seconds", Help: "End-to-end agent task time, including queue wait.",
			ConstLabels: app, Buckets: buckets,
		}),
		tasks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "agent_tasks_total", Help: "Agent tasks by result.", ConstLabels: app,
		}, []string{"result"}),
		llmCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "agent_llm_calls_total", Help: "LLM calls by outcome (ok, rate_limited, error).", ConstLabels: app,
		}, []string{"outcome"}),
		llmTokens: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "agent_llm_tokens_total", Help: "Tokens consumed by completed LLM calls.", ConstLabels: app,
		}),
		toolTimeouts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "agent_tool_timeouts_total", Help: "Tool calls that timed out.", ConstLabels: app,
		}),
		httpDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency in seconds.",
			ConstLabels: prometheus.Labels{"service": cfg.service}, Buckets: buckets,
		}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "HTTP requests by response status.",
			ConstLabels: prometheus.Labels{"service": cfg.service},
		}, []string{"status"}),
	}
	gauge := func(name, help string, f func() float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: app}, f)
	}
	reg.MustRegister(r.m.taskSeconds, r.m.tasks, r.m.llmCalls, r.m.llmTokens, r.m.toolTimeouts,
		r.m.httpDuration, r.m.httpRequests,
		gauge("agent_tasks_queued", "Tasks waiting for a free worker.",
			func() float64 { return float64(r.queued.Load()) }),
		gauge("agent_tasks_running", "Tasks currently being worked.",
			func() float64 { return float64(r.running.Load()) }),
		gauge("agent_tasks_in_flight", "Queued plus running tasks.",
			func() float64 { return float64(r.queued.Load() + r.running.Load()) }),
		gauge("agent_workers", "Worker slots in this pod.",
			func() float64 { return float64(cfg.workers) }),
	)
	return r
}

// handleTask runs one agent task synchronously: queue for a worker, call the
// LLM, call a tool. The HTTP status reports the task's outcome.
func (r *runner) handleTask(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	status := r.runTask(req.Context(), start)
	r.m.httpDuration.Observe(time.Since(start).Seconds())
	r.m.httpRequests.WithLabelValues(strconv.Itoa(status)).Inc()
	w.WriteHeader(status)
}

func (r *runner) runTask(ctx context.Context, start time.Time) int {
	if q := r.queued.Load(); q >= int64(r.cfg.maxQueue) {
		r.logs.println("queue-full", lineQueueFull(q))
		r.m.tasks.WithLabelValues("rejected").Inc()
		return http.StatusServiceUnavailable
	}
	r.queued.Add(1)
	select {
	case r.slots <- struct{}{}:
		r.queued.Add(-1)
	case <-ctx.Done():
		r.queued.Add(-1)
		r.m.tasks.WithLabelValues("abandoned").Inc()
		return http.StatusServiceUnavailable
	}
	r.running.Add(1)
	defer func() {
		r.running.Add(-1)
		<-r.slots
	}()

	if waited := time.Since(start); waited > time.Second {
		r.logs.println("pool", linePoolSaturated(r.cfg.workers, waited))
	}

	status, result := r.work(ctx)
	r.m.tasks.WithLabelValues(result).Inc()
	r.m.taskSeconds.Observe(time.Since(start).Seconds())
	return status
}

// work is the agent loop body: one LLM completion, then one tool call.
func (r *runner) work(ctx context.Context) (status int, result string) {
	if !r.callLLM(ctx) {
		return http.StatusBadGateway, "llm_failed"
	}
	toolTimeoutPct, overflowPct := r.faults.get()
	if rand.Intn(100) < overflowPct {
		r.logs.println("context", lineContextOverflow())
	}
	if rand.Intn(100) < toolTimeoutPct {
		sleepCtx(ctx, toolTimeout)
		r.m.toolTimeouts.Inc()
		r.logs.println("tool", lineToolTimeout("web-search", toolTimeout))
		return http.StatusGatewayTimeout, "tool_timeout"
	}
	sleepCtx(ctx, r.cfg.toolLatency)
	return http.StatusOK, "ok"
}

// callLLM asks the mock provider for a completion, backing off on 429.
func (r *runner) callLLM(ctx context.Context) bool {
	for attempt := 1; attempt <= r.cfg.llmAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.llmURL+"/v1/complete",
			bytes.NewReader([]byte(`{"prompt":"plan next step"}`)))
		if err != nil {
			r.m.llmCalls.WithLabelValues("error").Inc()
			return false
		}
		resp, err := r.client.Do(req)
		if err != nil {
			r.m.llmCalls.WithLabelValues("error").Inc()
			return false
		}
		var body struct{ Tokens int }
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
			r.m.llmCalls.WithLabelValues("ok").Inc()
			r.m.llmTokens.Add(float64(body.Tokens))
			return true
		case resp.StatusCode == http.StatusTooManyRequests:
			r.m.llmCalls.WithLabelValues("rate_limited").Inc()
			r.logs.println("429", lineRateLimited(attempt))
			sleepCtx(ctx, time.Duration(attempt)*250*time.Millisecond)
		default:
			r.m.llmCalls.WithLabelValues("error").Inc()
			return false
		}
	}
	return false
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// handleFault reads or sets the runner's injected failure modes.
func (r *runner) handleFault(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost {
		var f struct {
			ToolTimeoutPct     *int `json:"toolTimeoutPct"`
			ContextOverflowPct *int `json:"contextOverflowPct"`
		}
		if err := json.NewDecoder(req.Body).Decode(&f); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		r.faults.mu.Lock()
		if f.ToolTimeoutPct != nil {
			r.faults.ToolTimeoutPct = *f.ToolTimeoutPct
		}
		if f.ContextOverflowPct != nil {
			r.faults.ContextOverflowPct = *f.ContextOverflowPct
		}
		r.faults.mu.Unlock()
	}
	r.faults.mu.RLock()
	defer r.faults.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(r.faults)
}
