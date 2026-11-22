package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"sigs.k8s.io/yaml"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
)

// newTestPair wires a runner to an in-process mock LLM.
func newTestPair(t *testing.T, llm llmConfig, faults *runnerFaults) (*runner, *mockLLM) {
	t.Helper()
	l := newMockLLM(prometheus.NewRegistry(), llm)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/complete", l.handleComplete)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	r := newRunner(prometheus.NewRegistry(), runnerConfig{
		service: "agent-runner", workers: 2, maxQueue: 10, llmURL: srv.URL,
		llmAttempts: 3, toolLatency: time.Millisecond,
	}, faults)
	return r, l
}

// The agent-runner CRs read these exact series; renaming one silently breaks
// the demo, so pin names and the app label.
func TestRunner_ExposesAgenticMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := newRunner(reg, runnerConfig{service: "agent-runner", workers: 1, maxQueue: 1, llmURL: "http://unused"},
		&runnerFaults{})
	r.m.tasks.WithLabelValues("ok").Inc()
	r.m.llmCalls.WithLabelValues("ok").Inc()
	r.m.taskSeconds.Observe(1)
	r.m.httpDuration.Observe(1)
	r.m.httpRequests.WithLabelValues("200").Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	labelOf := map[string]map[string]string{}
	for _, mf := range families {
		labels := map[string]string{}
		for _, l := range mf.GetMetric()[0].GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		labelOf[mf.GetName()] = labels
	}
	for name, target := range map[string]string{
		"agent_tasks_queued": "app", "agent_tasks_running": "app", "agent_tasks_in_flight": "app",
		"agent_workers": "app", "agent_task_seconds": "app", "agent_tasks_total": "app",
		"agent_llm_calls_total": "app", "agent_llm_tokens_total": "app", "agent_tool_timeouts_total": "app",
		"http_request_duration_seconds": "service", "http_requests_total": "service",
	} {
		labels, ok := labelOf[name]
		if !ok {
			t.Errorf("missing metric %s", name)
			continue
		}
		if labels[target] != "agent-runner" {
			t.Errorf("%s: want %s=agent-runner, got %v", name, target, labels)
		}
	}
}

func TestRunner_HealthyTaskSucceeds(t *testing.T) {
	r, _ := newTestPair(t, llmConfig{rpm: 6000, latency: time.Millisecond}, &runnerFaults{})
	if got := r.runTask(context.Background(), time.Now()); got != http.StatusOK {
		t.Fatalf("status: want 200, got %d", got)
	}
	if v := testutil.ToFloat64(r.m.llmCalls.WithLabelValues("ok")); v != 1 {
		t.Errorf("ok LLM calls: want 1, got %v", v)
	}
	if v := testutil.ToFloat64(r.m.llmTokens); v != tokensPerCompletion {
		t.Errorf("tokens: want %d, got %v", tokensPerCompletion, v)
	}
}

// With the provider's quota exhausted every attempt is a 429: the runner
// retries llmAttempts times, records each as rate_limited, then fails the task.
func TestRunner_ProviderRateLimitFailsTaskAfterRetries(t *testing.T) {
	r, l := newTestPair(t, llmConfig{rpm: 1, latency: time.Millisecond}, &runnerFaults{})
	l.bucket = 0 // quota already spent
	if got := r.runTask(context.Background(), time.Now()); got != http.StatusBadGateway {
		t.Fatalf("status: want 502, got %d", got)
	}
	if v := testutil.ToFloat64(r.m.llmCalls.WithLabelValues("rate_limited")); v != 3 {
		t.Errorf("rate_limited calls: want 3, got %v", v)
	}
	if v := testutil.ToFloat64(r.m.tasks.WithLabelValues("llm_failed")); v != 1 {
		t.Errorf("llm_failed tasks: want 1, got %v", v)
	}
}

func TestRunner_ToolTimeoutFault(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 2 s tool timeout")
	}
	r, _ := newTestPair(t, llmConfig{rpm: 6000, latency: time.Millisecond}, &runnerFaults{ToolTimeoutPct: 100})
	if got := r.runTask(context.Background(), time.Now()); got != http.StatusGatewayTimeout {
		t.Fatalf("status: want 504, got %d", got)
	}
	if v := testutil.ToFloat64(r.m.toolTimeouts); v != 1 {
		t.Errorf("tool timeouts: want 1, got %v", v)
	}
}

func TestRunner_RejectsWhenQueueFull(t *testing.T) {
	r, _ := newTestPair(t, llmConfig{rpm: 6000, latency: time.Millisecond}, &runnerFaults{})
	r.queued.Store(int64(r.cfg.maxQueue))
	if got := r.runTask(context.Background(), time.Now()); got != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d", got)
	}
}

func TestMockLLM_SharedQuotaAndBudget(t *testing.T) {
	l := newMockLLM(prometheus.NewRegistry(), llmConfig{rpm: 60, budgetPerMin: 1600})
	now := time.Now()
	if !l.allow(now) {
		t.Fatal("first call within quota must pass")
	}
	if l.allow(now) {
		t.Fatal("60 rpm allows one call per second; a second call in the same instant must be limited")
	}
	if !l.allow(now.Add(1100 * time.Millisecond)) {
		t.Fatal("quota must refill after a second")
	}
	l.spend(now, tokensPerCompletion)
	if got := l.budgetUsedPercent(); got != 50 {
		t.Errorf("budget used: want 50%%, got %v", got)
	}
}

// Every line the runner emits must match the pattern its CRs declare for it,
// or the demo would silently stop exercising log ingestion and fusion.
func TestLogLines_MatchAgentRunnerCRPatterns(t *testing.T) {
	want := map[string]string{
		lineRateLimited(2):                          "llm-rate-limited",
		lineToolTimeout("web-search", toolTimeout):  "tool-timeout",
		lineContextOverflow():                       "context-overflow",
		linePoolSaturated(4, 2300*time.Millisecond): "connection-pool-exhausted",
	}
	crs := []string{"../../config/samples/dev-agent-runner-autoscaler.yaml", "../../examples/agent-runner.yaml"}
	for _, path := range crs {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var aa scalingv1alpha1.AgenticAutoscaler
		if err := yaml.Unmarshal(raw, &aa); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		patterns := map[string]*regexp.Regexp{}
		for _, p := range aa.Spec.LogPatterns {
			patterns[p.Name] = regexp.MustCompile(p.Regex)
		}
		for line, name := range want {
			re, ok := patterns[name]
			if !ok {
				t.Errorf("%s: no logPattern named %q", path, name)
				continue
			}
			if !re.MatchString(line) {
				t.Errorf("%s: %q does not match %s (%s)", path, line, name, re)
			}
			for other, ore := range patterns {
				if other != name && ore.MatchString(line) {
					t.Errorf("%s: %q also matches %s; keep the demo's signals distinct", path, line, other)
				}
			}
		}
	}
}
