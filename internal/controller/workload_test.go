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

package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

func patternNamesOf(ps []fusion.Pattern) map[string]bool {
	out := map[string]bool{}
	for _, p := range ps {
		out[p.Name] = true
	}
	return out
}

// Agentic built-ins exist only for CRs that configure spec.agentic, so a
// regular service's fused signal is exactly what it was before.
func TestPatternsFor_AgenticBuiltinsAreGated(t *testing.T) {
	plain, _ := patternsFor(scalingv1alpha1.AgenticAutoscalerSpec{})
	if len(plain) != len(fusion.DefaultPatterns) || patternNamesOf(plain)["llm-rate-limited"] {
		t.Fatalf("plain service must get only the defaults, got %v", patternNamesOf(plain))
	}

	spec := scalingv1alpha1.AgenticAutoscalerSpec{
		Agentic: scalingv1alpha1.AgenticConfig{LLMRateLimitQuery: "q"},
		LogPatterns: []scalingv1alpha1.LogPatternSpec{
			{Name: "tool-timeout", Regex: "my tool hung", Severity: "critical", Score: 90},
		},
	}
	agentic, _ := patternsFor(spec)
	names := patternNamesOf(agentic)
	for _, p := range fusion.AgenticPatterns {
		if !names[p.Name] {
			t.Errorf("agentic CR missing built-in %s", p.Name)
		}
	}
	for _, p := range agentic {
		if p.Name == "tool-timeout" && p.Re.String() != "my tool hung" {
			t.Errorf("spec.logPatterns must override an agentic built-in, got %s", p.Re)
		}
	}
}

func TestApplyWorkloadConfig_ArmsGuardsOnlyWithTheirQuery(t *testing.T) {
	var cfg reasoning.Config
	applyWorkloadConfig(&cfg, scalingv1alpha1.AgenticAutoscalerSpec{
		Backlog: scalingv1alpha1.BacklogConfig{TargetPerReplica: 10}, // no query
		Agentic: scalingv1alpha1.AgenticConfig{RateLimitHoldPercent: 30},
	})
	if cfg.BacklogPerReplica != 0 || cfg.RateLimitHoldPct != 0 || cfg.TokenBudgetCapPct != 0 {
		t.Errorf("thresholds without queries must stay disarmed: %+v", cfg)
	}

	applyWorkloadConfig(&cfg, scalingv1alpha1.AgenticAutoscalerSpec{
		Backlog: scalingv1alpha1.BacklogConfig{Query: "q", TargetPerReplica: 10},
		Agentic: scalingv1alpha1.AgenticConfig{LLMRateLimitQuery: "q", TokenBudgetQuery: "q"},
	})
	if cfg.BacklogPerReplica != 10 || cfg.RateLimitHoldPct != defaultRateLimitHoldPercent ||
		cfg.TokenBudgetCapPct != defaultTokenBudgetCapPercent {
		t.Errorf("want backlog 10 and default thresholds, got %+v", cfg)
	}
}

// The guard binds every provider: an LLM asking to scale into a provider
// rate limit is held, the Deployment is untouched, and the status says why.
func TestReconcile_LLMScaleUpHeldByProviderRateLimit(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("agents", "default", "agent-runner")
	aa.Spec.Agentic.LLMRateLimitQuery = `100 * sum(rate(agent_llm_calls_total{outcome="rate_limited"}[2m]))`
	llm := reasoning.ScaleDecision{Action: "scale-up", TargetReplicas: 9, Confidence: 0.8,
		Provider: "anthropic", Reason: "tasks are queueing", Timestamp: newTimestamp(time.Now())}
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, makeDeployment("agent-runner", "default", 3)}, llm)
	limited := 45.0
	rec.SignalCollector = &snapshotSignalCollector{snap: signals.SystemSnapshot{
		Metrics: signals.MetricSnapshot{LLMRateLimitedPct: &limited},
	}}

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "agents")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	deploy := &appsv1.Deployment{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "agent-runner"}, deploy)
	if *deploy.Spec.Replicas != 3 {
		t.Errorf("held decision must not scale: replicas %d", *deploy.Spec.Replicas)
	}
	got := getAA(t, rec, "agents")
	if !strings.Contains(got.Status.LastDecisionReason, "rate-limiting 45%") {
		t.Errorf("status must explain the hold, got %q", got.Status.LastDecisionReason)
	}
}

// Optional signals fill their snapshot field when their query works, and a
// failing one is left nil for the cycle instead of failing the reconcile.
func TestCollectWorkloadSignals_OptionalAndFailSafe(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch q := r.URL.Query().Get("query"); {
		case strings.Contains(q, "broken"):
			http.Error(w, "bad query", http.StatusBadRequest)
		case strings.Contains(q, `app="agent-runner"`):
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"37"]}]}}`)
		default:
			t.Errorf("$TARGET not substituted: %s", q)
		}
	}))
	defer prom.Close()

	spec := scalingv1alpha1.AgenticAutoscalerSpec{
		TargetDeployment: "agent-runner",
		Backlog:          scalingv1alpha1.BacklogConfig{Query: `sum(q{app="$TARGET"})`, TargetPerReplica: 10},
		Agentic: scalingv1alpha1.AgenticConfig{
			LLMRateLimitQuery: `rl{app="$TARGET"}`,
			TokenBudgetQuery:  `broken{app="$TARGET"}`,
		},
	}
	var m signals.MetricSnapshot
	collectWorkloadSignals(context.Background(), signals.NewPrometheusCollector(prom.URL), spec, &m)

	if m.Backlog == nil || *m.Backlog != 37 || m.LLMRateLimitedPct == nil || *m.LLMRateLimitedPct != 37 {
		t.Errorf("working signals must be set: backlog=%v ratelimit=%v", m.Backlog, m.LLMRateLimitedPct)
	}
	if m.TokenBudgetUsedPct != nil {
		t.Errorf("a failing optional query must leave its signal nil, got %v", *m.TokenBudgetUsedPct)
	}

	var none signals.MetricSnapshot
	collectWorkloadSignals(context.Background(), signals.NewPrometheusCollector(prom.URL),
		scalingv1alpha1.AgenticAutoscalerSpec{TargetDeployment: "x"}, &none)
	if none.Backlog != nil || none.LLMRateLimitedPct != nil || none.TokenBudgetUsedPct != nil {
		t.Error("unconfigured signals must stay nil")
	}
}
