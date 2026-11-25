package reasoning

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

func f64(v float64) *float64 { return &v }

func decideRules(t *testing.T, cfg Config, m signals.MetricSnapshot, patterns ...string) ScaleDecision {
	t.Helper()
	d, err := NewRuleBasedAgent(cfg).Decide(context.Background(), makeSignal(m, patterns...))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func agentCfg() Config {
	return Config{CurrentReplicas: 2, MinReplicas: 2, MaxReplicas: 12, BacklogPerReplica: 10,
		RateLimitHoldPct: 20, TokenBudgetCapPct: 90}
}

func TestRuleB_BacklogScalesToFitWithinMax(t *testing.T) {
	cases := []struct {
		backlog float64
		want    int32
	}{
		{45, 5},   // ceil(45/10)
		{180, 12}, // ceil(180/10)=18, clamped to max
	}
	for _, tc := range cases {
		d := decideRules(t, agentCfg(), signals.MetricSnapshot{Backlog: f64(tc.backlog), CPUUtilPct: 3})
		if d.Action != actionScaleUp || d.TargetReplicas != tc.want || d.Confidence != 0.85 {
			t.Errorf("backlog %v: want scale-up to %d @0.85, got %s %d @%.2f (%s)",
				tc.backlog, tc.want, d.Action, d.TargetReplicas, d.Confidence, d.Reason)
		}
		if !strings.Contains(d.Reason, "backlog of") {
			t.Errorf("reason must name the backlog: %q", d.Reason)
		}
	}
}

func TestRuleB_QuietWhenBacklogFitsOrIsUnconfigured(t *testing.T) {
	// Fits: 15 queued over 2 replicas at 10 each.
	if d := decideRules(t, agentCfg(), signals.MetricSnapshot{Backlog: f64(15)}); d.Action != ActionHold {
		t.Errorf("fitting backlog: want hold, got %s %d", d.Action, d.TargetReplicas)
	}
	// A backlog value without a per-replica target never drives scaling...
	noTarget := agentCfg()
	noTarget.BacklogPerReplica = 0
	if d := decideRules(t, noTarget, signals.MetricSnapshot{Backlog: f64(500)}); d.Action != ActionHold {
		t.Errorf("no targetPerReplica: want hold, got %s", d.Action)
	}
	// ...and neither does a target without a backlog signal.
	if d := decideRules(t, agentCfg(), signals.MetricSnapshot{}); d.Action != ActionHold {
		t.Errorf("no backlog signal: want hold, got %s", d.Action)
	}
}

func TestRule5_DoesNotReleaseCapacityTheBacklogNeeds(t *testing.T) {
	cfg := agentCfg()
	cfg.CurrentReplicas = 8
	cfg.CleanSince = cleanSince(20 * time.Minute)

	// 50 queued needs 5 replicas > min+1 = 3: stay at 8.
	if d := decideRules(t, cfg, signals.MetricSnapshot{Backlog: f64(50), CPUUtilPct: 2}); d.Action != ActionHold {
		t.Errorf("backlog still needs capacity: want hold, got %s %d", d.Action, d.TargetReplicas)
	}
	// Drained: scale down to min+1 as before.
	d := decideRules(t, cfg, signals.MetricSnapshot{Backlog: f64(0), CPUUtilPct: 2})
	if d.Action != actionScaleDown || d.TargetReplicas != 3 {
		t.Errorf("drained backlog: want scale-down to 3, got %s %d", d.Action, d.TargetReplicas)
	}
}

// A provider rate limit is shared by every replica, so an error rate driven by
// 429s must not trigger rule 3's scale-up — and the hold must say why.
func TestGuard_ProviderRateLimitHoldsScaleUp(t *testing.T) {
	m := signals.MetricSnapshot{ErrorRatePct: 40, LLMRateLimitedPct: f64(35), Backlog: f64(120)}
	d := decideRules(t, agentCfg(), m, "llm-rate-limited")
	if d.Action != ActionHold {
		t.Fatalf("want hold under provider rate limiting, got %s %d", d.Action, d.TargetReplicas)
	}
	for _, want := range []string{"rate-limiting 35%", "shared by every replica", "would have scaled to"} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("reason %q missing %q", d.Reason, want)
		}
	}
	if d.Provider != "rule-based" {
		t.Errorf("provider: want rule-based, got %q", d.Provider)
	}
}

func TestGuard_BelowThresholdsScalesNormally(t *testing.T) {
	m := signals.MetricSnapshot{Backlog: f64(60), LLMRateLimitedPct: f64(5), TokenBudgetUsedPct: f64(40)}
	if d := decideRules(t, agentCfg(), m); d.Action != actionScaleUp || d.TargetReplicas != 6 {
		t.Errorf("healthy provider: want backlog scale-up to 6, got %s %d", d.Action, d.TargetReplicas)
	}
}

func TestGuard_TokenBudgetCapBlocksScaleUp(t *testing.T) {
	d := decideRules(t, agentCfg(), signals.MetricSnapshot{Backlog: f64(90), TokenBudgetUsedPct: f64(95)})
	if d.Action != ActionHold || !strings.Contains(d.Reason, "cost guardrail") {
		t.Errorf("want cost-guardrail hold, got %s: %s", d.Action, d.Reason)
	}
}

func TestGuard_OOMAndScaleDownAreNeverBlocked(t *testing.T) {
	limited := signals.MetricSnapshot{LLMRateLimitedPct: f64(80), TokenBudgetUsedPct: f64(99)}
	if d := decideRules(t, agentCfg(), limited, "oom-killed"); d.Action != actionScaleUp {
		t.Errorf("OOM is an emergency: want scale-up, got %s", d.Action)
	}

	down := ScaleDecision{Action: actionScaleDown, TargetReplicas: 3}
	if got := GuardWorkload(agentCfg(), makeSignal(limited), down); got.Action != actionScaleDown {
		t.Errorf("guards only block scale-ups, got %s", got.Action)
	}
}

// Any provider's decision is guarded the same way (the controller applies it
// after an LLM), and confidence/provider survive so the audit stays truthful.
func TestGuard_AppliesToLLMDecisions(t *testing.T) {
	llm := ScaleDecision{Action: actionScaleUp, TargetReplicas: 9, Confidence: 0.8,
		Provider: providerAnthropic, Reason: "tasks queueing"}
	got := GuardWorkload(agentCfg(), makeSignal(signals.MetricSnapshot{LLMRateLimitedPct: f64(50)}), llm)
	if got.Action != ActionHold || got.Provider != providerAnthropic || got.Confidence != 0.8 ||
		!strings.Contains(got.Reason, "would have scaled to 9 replicas: tasks queueing") {
		t.Errorf("guarded LLM decision: %+v", got)
	}
}

// With no workload signals and no guards armed, nothing changes: the
// regression guarantee for every non-agent service.
func TestGuard_DisabledWithoutSignals(t *testing.T) {
	up := ScaleDecision{Action: actionScaleUp, TargetReplicas: 7}
	got := GuardWorkload(baseConfig, makeSignal(signals.MetricSnapshot{ErrorRatePct: 50}), up)
	if got.Action != up.Action || got.TargetReplicas != 7 {
		t.Errorf("unconfigured guard changed the decision: %+v", got)
	}
	// A signal without an armed threshold does nothing either.
	got = GuardWorkload(baseConfig, makeSignal(signals.MetricSnapshot{LLMRateLimitedPct: f64(99)}), up)
	if got.Action != up.Action || got.TargetReplicas != 7 {
		t.Errorf("unarmed guard changed the decision: %+v", got)
	}
}

func TestPrompt_WorkloadSignalsOnlyWhenConfigured(t *testing.T) {
	plain := BuildPrompt(baseConfig, makeSignal(signals.MetricSnapshot{LatencyP99Ms: 120}))
	if strings.Contains(plain, "Workload signals") {
		t.Errorf("prompt for a plain service must be unchanged:\n%s", plain)
	}
	agentic := BuildPrompt(agentCfg(), makeSignal(signals.MetricSnapshot{
		Backlog: f64(42), LLMRateLimitedPct: f64(12.5), TokenBudgetUsedPct: f64(61),
	}))
	for _, want := range []string{"Queued work items: 42 (target 10 per replica)",
		"rate limit: 12.5% (hold at 20%)", "Token budget used: 61.0% (scale-up cap 90%)", "shared by every replica"} {
		if !strings.Contains(agentic, want) {
			t.Errorf("agentic prompt missing %q:\n%s", want, agentic)
		}
	}
}
