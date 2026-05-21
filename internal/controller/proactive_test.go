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

// TestProactiveScaling is the conference demo "money shot":
//
// A Deployment sits at 3 replicas. CPU is 45% — well below the 70% threshold
// that HPA uses. Under pure HPA, nothing would happen. But the log stream
// already shows connection pool exhaustion and p99 latency is 1800ms.
//
// The agentic autoscaler detects the log pattern, decides to scale up *now*,
// and patches the Deployment — all before CPU climbs high enough to wake HPA.
//
// This test verifies each pipeline stage and prints a timeline you can show
// on screen during a live demo.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/observability"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/policy"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/scaler"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

func TestProactiveScaling(t *testing.T) {
	const (
		// Scenario parameters
		initialReplicas  = int32(3)
		cpuUtilPct       = 45.0  // current CPU — HPA would not fire
		hpaCPUThreshold  = 70.0  // HPA fires only when CPU exceeds this
		latencyP99Ms     = 1800.0
		logLine          = "connection pool exhausted: max connections reached"
		targetDeployment = "payment-service"
		namespace        = "demo"

		// Expected outcome
		wantReplicas = int32(5) // scaleUpByFraction(3, 0.50, 20): add=ceil(1.5)=2, target=5
	)

	timeline := &demoTimeline{}

	// -------------------------------------------------------------------------
	// Stage 0 — describe the scenario
	// -------------------------------------------------------------------------
	timeline.record("SCENARIO",
		fmt.Sprintf("Deployment '%s' running at %d replicas", targetDeployment, initialReplicas),
		fmt.Sprintf("CPU: %.0f%%  (HPA threshold: %.0f%% — HPA is DORMANT)", cpuUtilPct, hpaCPUThreshold),
		fmt.Sprintf("Log: %q", logLine),
		fmt.Sprintf("p99 latency: %.0fms", latencyP99Ms),
	)

	// -------------------------------------------------------------------------
	// Stage 1 — build the system snapshot as the signal collector would return
	// -------------------------------------------------------------------------
	snap := signals.SystemSnapshot{
		Timestamp: time.Now(),
		Metrics: signals.MetricSnapshot{
			LatencyP99Ms: latencyP99Ms,
			CPUUtilPct:   cpuUtilPct,
			ErrorRatePct: 0,
		},
		LogEntries: []signals.LogEntry{
			{Timestamp: time.Now().Add(-30 * time.Second), Message: logLine},
		},
	}

	timeline.record("STAGE 1 — Signal collection",
		"Prometheus: p99=1800ms, cpu=45%, errorRate=0%",
		"Loki: 1 log line retrieved in past 60s",
	)

	// -------------------------------------------------------------------------
	// Stage 2 — fuse signals: verify the correlator detects the log pattern
	// -------------------------------------------------------------------------
	fused := fusion.Correlate(snap)

	if !fused.HasPattern("connection-pool-exhausted") {
		t.Errorf("fusion: expected pattern 'connection-pool-exhausted' in MatchedPatterns, got %v",
			patternNamesFromFusedMatches(fused.MatchedPatterns))
	}
	if fused.SeverityScore <= 0.5 {
		t.Errorf("fusion: SeverityScore should be > 0.5 for connection-pool-exhausted, got %.2f", fused.SeverityScore)
	}
	if cpuUtilPct >= hpaCPUThreshold {
		t.Fatalf("scenario misconfigured: CPU %.0f%% must be below HPA threshold %.0f%%", cpuUtilPct, hpaCPUThreshold)
	}

	timeline.record("STAGE 2 — Signal fusion (pkg/fusion)",
		fmt.Sprintf("Pattern matched : connection-pool-exhausted (score=%.2f)", fused.SeverityScore),
		fmt.Sprintf("HPA CPU check   : %.0f%% < %.0f%% threshold → HPA still DORMANT", cpuUtilPct, hpaCPUThreshold),
		"Agentic signal  : CRITICAL log pattern detected",
	)

	// -------------------------------------------------------------------------
	// Stage 3 — reasoning: the AI agent decides to scale up
	//
	// latency=1800ms is below the rule-based threshold of 2000ms, so we use a
	// fixedAgent representing the AI model's decision. This is intentional: the
	// value of the AI path is that it weighs sub-threshold signals together and
	// acts earlier than deterministic rules allow.
	// -------------------------------------------------------------------------
	ts := metav1.NewTime(time.Now())
	agentDecision := reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: wantReplicas,
		Confidence:     0.87,
		Reason:         "connection pool exhausted in logs; latency rising toward SLO breach — scale up proactively",
		Provider:       "anthropic",
		Timestamp:      &ts,
	}

	timeline.record("STAGE 3 — AI reasoning (pkg/reasoning)",
		fmt.Sprintf("Provider  : %s", agentDecision.Provider),
		fmt.Sprintf("Decision  : %s → %d replicas (confidence=%.0f%%)", agentDecision.Action, agentDecision.TargetReplicas, agentDecision.Confidence*100),
		fmt.Sprintf("Reason    : %s", agentDecision.Reason),
		"HPA verdict: would not have fired — CPU still below threshold",
	)

	// -------------------------------------------------------------------------
	// Stage 4 — full reconciler run against a fake kube API
	// -------------------------------------------------------------------------
	s := reconcilerScheme(t)

	aa := &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "autoscaler", Namespace: namespace},
		Spec: scalingv1alpha1.AgenticAutoscalerSpec{
			TargetDeployment: targetDeployment,
			Namespace:        namespace,
			PrometheusURL:    "http://prometheus:9090",
			LogSource:        scalingv1alpha1.LogSourceConfig{Type: "loki"},
			MinReplicas:      2,
			MaxReplicas:      20,
			CooldownSeconds:  60,
			DryRun:           false,
		},
	}
	deploy := makeDeployment(targetDeployment, namespace, initialReplicas)

	fc := proactiveFakeClient(t, s, aa, deploy)

	rec := &AgenticAutoscalerReconciler{
		Client:         fc,
		Scheme:         s,
		SignalCollector: &staticSignalCollector{snapshot: snap},
		Policy:         policy.NewEnforcer(&noopHPAReader{}),
		Scaler:         scaler.NewExecutor(fc),
		Observability:  observability.NewNoopRecorder(),
		NewAgent: func(_ reasoning.Config) reasoning.Agent {
			return &fixedAgent{decision: agentDecision}
		},
	}

	ctx := context.Background()
	_, err := rec.Reconcile(ctx, reconcileRequest(namespace, "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// -------------------------------------------------------------------------
	// Stage 5 — verify the Deployment was patched and status was recorded
	// -------------------------------------------------------------------------
	updatedDeploy := &appsv1.Deployment{}
	if err := fc.Get(ctx, types.NamespacedName{Name: targetDeployment, Namespace: namespace}, updatedDeploy); err != nil {
		t.Fatalf("Get Deployment after reconcile: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != wantReplicas {
		t.Errorf("Deployment replicas: want %d, got %d", wantReplicas, *updatedDeploy.Spec.Replicas)
	}

	updatedAA := &scalingv1alpha1.AgenticAutoscaler{}
	if err := fc.Get(ctx, types.NamespacedName{Name: "autoscaler", Namespace: namespace}, updatedAA); err != nil {
		t.Fatalf("Get AgenticAutoscaler after reconcile: %v", err)
	}
	if !strings.Contains(strings.ToLower(updatedAA.Status.LastDecisionReason), "connection pool") {
		t.Errorf("LastDecisionReason should mention 'connection pool', got: %q", updatedAA.Status.LastDecisionReason)
	}

	timeline.record("STAGE 4 — Controller reconcile + scale execution",
		fmt.Sprintf("Deployment patched : %d → %d replicas", initialReplicas, *updatedDeploy.Spec.Replicas),
		fmt.Sprintf("Status reason      : %q", updatedAA.Status.LastDecisionReason),
		fmt.Sprintf("Scale happened     : BEFORE CPU reached %.0f%% HPA threshold", hpaCPUThreshold),
	)

	timeline.record("OUTCOME",
		fmt.Sprintf("Agentic autoscaler scaled '%s' from %d → %d replicas", targetDeployment, initialReplicas, wantReplicas),
		fmt.Sprintf("Triggered by log pattern at CPU=%.0f%% — HPA at %.0f%% threshold never woke up", cpuUtilPct, hpaCPUThreshold),
		"Decision is explainable: reason recorded in CR status",
		"Pipeline: signals → fusion → AI → policy → scale",
	)

	// Print the full timeline so it renders clearly under `go test -v`.
	fmt.Print(timeline.render())
}

// ---- demo timeline ----------------------------------------------------------

type timelineStage struct {
	title string
	lines []string
}

type demoTimeline struct {
	stages []timelineStage
}

func (d *demoTimeline) record(title string, lines ...string) {
	d.stages = append(d.stages, timelineStage{title: title, lines: lines})
}

func (d *demoTimeline) render() string {
	const width = 68
	bar := strings.Repeat("─", width)
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("┌" + bar + "┐\n")
	b.WriteString("│" + padCenter("PROACTIVE AUTOSCALING DEMO", width) + "│\n")
	b.WriteString("│" + padCenter("act on logs before CPU triggers HPA", width) + "│\n")
	b.WriteString("└" + bar + "┘\n\n")

	for i, stage := range d.stages {
		b.WriteString(fmt.Sprintf("  [%d] %s\n", i, stage.title))
		for _, l := range stage.lines {
			b.WriteString(fmt.Sprintf("      %s\n", l))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func padCenter(s string, width int) string {
	if len(s) >= width {
		return s
	}
	left := (width - len(s)) / 2
	right := width - left - len(s)
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

func patternNamesFromFusedMatches(matches []fusion.PatternMatch) []string {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name
	}
	return names
}

// proactiveFakeClient builds a fake controller-runtime client pre-loaded with
// the given objects, with status-subresource support for AgenticAutoscaler.
func proactiveFakeClient(t *testing.T, s *runtime.Scheme, objs ...runtime.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{}).
		WithRuntimeObjects(objs...).
		Build()
}
