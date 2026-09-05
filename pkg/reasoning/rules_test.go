package reasoning

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

// cleanSince returns a *metav1.Time set d before now — the durable quiet-window
// anchor the controller supplies to the agent via Config.CleanSince.
func cleanSince(d time.Duration) *metav1.Time {
	t := metav1.NewTime(time.Now().Add(-d))
	return &t
}

// ---- test helpers ----------------------------------------------------------

// baseConfig is a standard Config used across tests unless overridden.
// cur=5, min=2, max=20 gives predictable scaleUpByFraction results:
//
//	+50%: ceil(5*0.5)=3 → target 8
//	+30%: ceil(5*0.3)=2 → target 7
//	scale-down: min+1 = 3
var baseConfig = Config{
	CurrentReplicas: 5,
	MinReplicas:     2,
	MaxReplicas:     20,
}

// makeSignal builds a FusedSignal from metric values and zero or more pattern names.
// SeverityScore is set to a non-zero value when any pattern is present; zero otherwise.
func makeSignal(m signals.MetricSnapshot, patternNames ...string) fusion.FusedSignal {
	patterns := make([]fusion.PatternMatch, len(patternNames))
	var maxScore float64
	for i, name := range patternNames {
		severity, score := severityAndScore(name)
		patterns[i] = fusion.PatternMatch{Name: name, Severity: severity, Count: 1}
		if score > maxScore {
			maxScore = score
		}
	}
	return fusion.FusedSignal{
		Snapshot:        signals.SystemSnapshot{Metrics: m},
		MatchedPatterns: patterns,
		SeverityScore:   maxScore,
	}
}

// severityAndScore returns the canonical severity string and score for a known
// pattern name, matching DefaultPatterns in pkg/fusion.
func severityAndScore(name string) (string, float64) {
	switch name {
	case "oom-killed":
		return "critical", 0.9
	case "connection-pool-exhausted":
		return "critical", 0.8
	case "db-connection-failed":
		return "critical", 0.7
	case "circuit-breaker-open":
		return "warning", 0.6
	case "upstream-timeout":
		return "warning", 0.5
	default:
		return "warning", 0.5
	}
}

// newAgent returns a *RuleBasedAgent (concrete type) so tests can set the
// unexported cfg (e.g. cfg.CleanSince) without going through the Agent interface.
func newAgent(cfg Config) *RuleBasedAgent {
	return &RuleBasedAgent{cfg: cfg}
}

// ---- table-driven tests ----------------------------------------------------

func TestRuleBasedAgent_Decide(t *testing.T) {
	tests := []struct {
		name             string
		cfg              Config
		signal           fusion.FusedSignal
		setupAgent       func(*RuleBasedAgent)
		wantAction       string
		wantTarget       int32
		wantConfidence   float64
		wantReasonContains string
	}{
		// --- Rule 1: OOM-killed --------------------------------------------------
		{
			name:             "oom-killed → scale-up 50%, confidence 0.95",
			cfg:              baseConfig,
			signal:           makeSignal(signals.MetricSnapshot{LatencyP99Ms: 800, CPUUtilPct: 70}, "oom-killed"),
			wantAction:       "scale-up",
			wantTarget:       8, // 5 + ceil(5*0.5)=3
			wantConfidence:   0.95,
			wantReasonContains: "OOMKilled",
		},
		{
			name: "oom-killed takes priority over circuit-breaker-open",
			cfg:  baseConfig,
			signal: makeSignal(
				signals.MetricSnapshot{LatencyP99Ms: 400},
				"oom-killed", "circuit-breaker-open",
			),
			wantAction:     "scale-up",
			wantTarget:     8,
			wantConfidence: 0.95, // rule 1 fires, not rule 4
		},
		{
			name: "oom-killed takes priority even with low latency",
			cfg:  baseConfig,
			signal: makeSignal(
				signals.MetricSnapshot{LatencyP99Ms: 10, CPUUtilPct: 5},
				"oom-killed",
			),
			wantAction:     "scale-up",
			wantTarget:     8,
			wantConfidence: 0.95,
		},

		// --- Rule 2: connection-pool-exhausted + high latency --------------------
		{
			name: "connection-pool-exhausted + latency > 2000ms → scale-up 50%",
			cfg:  baseConfig,
			signal: makeSignal(
				signals.MetricSnapshot{LatencyP99Ms: 2500, ErrorRatePct: 8},
				"connection-pool-exhausted",
			),
			wantAction:       "scale-up",
			wantTarget:       8,
			wantConfidence:   0.90,
			wantReasonContains: "connection pool",
		},
		{
			name: "connection-pool-exhausted + latency ≤ 2000ms → rule 2 skips, falls to hold",
			cfg:  baseConfig,
			signal: makeSignal(
				signals.MetricSnapshot{LatencyP99Ms: 1999, ErrorRatePct: 3},
				"connection-pool-exhausted",
			),
			wantAction: "hold", // latency not high enough; no other rules match
		},

		// --- Rule 3: error rate --------------------------------------------------
		{
			name:             "error rate > 10% → scale-up 30%, confidence 0.80",
			cfg:              baseConfig,
			signal:           makeSignal(signals.MetricSnapshot{ErrorRatePct: 15}),
			wantAction:       "scale-up",
			wantTarget:       7, // 5 + ceil(5*0.3)=2
			wantConfidence:   0.80,
			wantReasonContains: "error rate",
		},
		{
			name:   "error rate exactly 10% → does not fire (must be strictly > 10)",
			cfg:    baseConfig,
			signal: makeSignal(signals.MetricSnapshot{ErrorRatePct: 10}),
			wantAction: "hold",
		},

		// --- Rule 4: circuit-breaker-open ----------------------------------------
		{
			name:             "circuit-breaker-open → scale-up 30%, confidence 0.70",
			cfg:              baseConfig,
			signal:           makeSignal(signals.MetricSnapshot{LatencyP99Ms: 300}, "circuit-breaker-open"),
			wantAction:       "scale-up",
			wantTarget:       7,
			wantConfidence:   0.70,
			wantReasonContains: "circuit breaker",
		},

		// --- Rule 5: scale-down after 15+ min quiet window -----------------------
		{
			name: "severity=0, CPU<20%, quiet ≥ 15 min → scale-down to min+1",
			cfg:  baseConfig,
			signal: makeSignal(signals.MetricSnapshot{
				LatencyP99Ms: 40, ErrorRatePct: 0, CPUUtilPct: 12,
			}),
			setupAgent: func(a *RuleBasedAgent) {
				a.cfg.CleanSince = cleanSince(20 * time.Minute)
			},
			wantAction:       "scale-down",
			wantTarget:       3, // min+1 = 2+1
			wantConfidence:   0.60,
			wantReasonContains: "15+ minutes",
		},
		{
			name: "severity=0, CPU<20%, quiet < 15 min → hold (window not reached)",
			cfg:  baseConfig,
			signal: makeSignal(signals.MetricSnapshot{
				LatencyP99Ms: 40, ErrorRatePct: 0, CPUUtilPct: 12,
			}),
			setupAgent: func(a *RuleBasedAgent) {
				a.cfg.CleanSince = cleanSince(5 * time.Minute)
			},
			wantAction: "hold",
		},
		{
			name: "CPU ≥ 20%, quiet ≥ 15 min → hold (CPU too high)",
			cfg:  baseConfig,
			signal: makeSignal(signals.MetricSnapshot{
				LatencyP99Ms: 40, CPUUtilPct: 35,
			}),
			setupAgent: func(a *RuleBasedAgent) {
				a.cfg.CleanSince = cleanSince(20 * time.Minute)
			},
			wantAction: "hold",
		},
		{
			name: "already at min+1 replicas, 15 min quiet → hold (no-op scale-down)",
			cfg: Config{CurrentReplicas: 3, MinReplicas: 2, MaxReplicas: 20},
			signal: makeSignal(signals.MetricSnapshot{
				LatencyP99Ms: 40, CPUUtilPct: 10,
			}),
			setupAgent: func(a *RuleBasedAgent) {
				a.cfg.CleanSince = cleanSince(20 * time.Minute)
			},
			wantAction: "hold", // target would be 3, cur is 3 — nothing to do
		},

		// --- Rule 6: default hold ------------------------------------------------
		{
			name:       "no conditions met → hold",
			cfg:        baseConfig,
			signal:     makeSignal(signals.MetricSnapshot{LatencyP99Ms: 200, CPUUtilPct: 50}),
			wantAction: "hold",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := newAgent(tc.cfg)
			if tc.setupAgent != nil {
				tc.setupAgent(agent)
			}

			got, err := agent.Decide(context.Background(), tc.signal)
			if err != nil {
				t.Fatalf("Decide: unexpected error: %v", err)
			}

			if got.Action != tc.wantAction {
				t.Errorf("Action: want %q, got %q", tc.wantAction, got.Action)
			}
			if tc.wantTarget != 0 && got.TargetReplicas != tc.wantTarget {
				t.Errorf("TargetReplicas: want %d, got %d", tc.wantTarget, got.TargetReplicas)
			}
			if tc.wantConfidence != 0 && got.Confidence != tc.wantConfidence {
				t.Errorf("Confidence: want %.2f, got %.2f", tc.wantConfidence, got.Confidence)
			}
			if tc.wantReasonContains != "" && !strings.Contains(got.Reason, tc.wantReasonContains) {
				t.Errorf("Reason: want substring %q in %q", tc.wantReasonContains, got.Reason)
			}
			if got.Timestamp == nil {
				t.Error("Timestamp: want non-nil, got nil")
			}
		})
	}
}

// ---- quiet-window timer contract -------------------------------------------
//
// The quiet-window timer now lives on the CR status (supplied to the agent via
// Config.CleanSince) and is advanced by the controller — not inside the agent.
// These tests pin the agent's half of that contract: it reads CleanSince but
// never mutates it, and without a durable CleanSince it can never scale down.

func TestRuleBasedAgent_NilCleanSince_NeverScalesDown(t *testing.T) {
	// A fresh agent each reconcile (as the controller builds it) with no durable
	// CleanSince must never scale down, no matter how many clean reconciles pass.
	// This is exactly the bug the durable status field fixes — with an in-memory
	// timer reset every reconcile, rule 5 was unreachable.
	clean := makeSignal(signals.MetricSnapshot{CPUUtilPct: 10, LatencyP99Ms: 40})
	for i := 0; i < 3; i++ {
		agent := NewRuleBasedAgent(baseConfig) // CleanSince nil
		got, err := agent.Decide(context.Background(), clean)
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if got.Action == "scale-down" {
			t.Fatalf("reconcile %d: fresh agent with nil CleanSince must not scale down, got %q", i, got.Action)
		}
	}
}

func TestRuleBasedAgent_DoesNotMutateCleanSince(t *testing.T) {
	// The agent reads CleanSince but must not change it — the controller owns the
	// timer. Deciding on a pattern-bearing signal must leave cfg.CleanSince intact.
	set := cleanSince(20 * time.Minute)
	agent := newAgent(baseConfig)
	agent.cfg.CleanSince = set

	_, _ = agent.Decide(context.Background(), makeSignal(
		signals.MetricSnapshot{CPUUtilPct: 10}, "upstream-timeout",
	))

	if agent.cfg.CleanSince != set {
		t.Errorf("agent must not mutate cfg.CleanSince; want %v, got %v", set, agent.cfg.CleanSince)
	}
}

func TestRuleBasedAgent_ScaleUpClampsToMax(t *testing.T) {
	// cur=18, max=20 → +50% would be 27, must clamp to 20.
	cfg := Config{CurrentReplicas: 18, MinReplicas: 2, MaxReplicas: 20}
	agent := newAgent(cfg)

	got, err := agent.Decide(context.Background(), makeSignal(
		signals.MetricSnapshot{LatencyP99Ms: 3000},
		"connection-pool-exhausted",
	))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.TargetReplicas != 20 {
		t.Errorf("TargetReplicas: want 20 (clamped to max), got %d", got.TargetReplicas)
	}
}

func TestRuleBasedAgent_ReasonIncludesReplicaDelta(t *testing.T) {
	agent := newAgent(baseConfig)
	got, _ := agent.Decide(context.Background(), makeSignal(
		signals.MetricSnapshot{LatencyP99Ms: 2500},
		"connection-pool-exhausted",
	))
	// decision.go appends "(replicas N → M)" to all scale decisions.
	if !strings.Contains(got.Reason, "replicas 5 → 8") {
		t.Errorf("Reason should contain replica delta, got %q", got.Reason)
	}
}
