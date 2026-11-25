package policy

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

func from(cur int32, d reasoning.ScaleDecision) reasoning.ScaleDecision {
	d.OldReplicas = cur
	return d
}

func enforce(t *testing.T, maxStep int32, d reasoning.ScaleDecision) reasoning.ScaleDecision {
	t.Helper()
	spec := baseSpec()
	spec.Policy.MaxScaleStep = maxStep
	got, err := NewEnforcer(nil).Enforce(context.Background(), spec, nil, d)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMaxScaleStep_CapsBothDirections(t *testing.T) {
	up := enforce(t, 3, from(4, scaleUp(16)))
	if up.TargetReplicas != 7 || up.Guardrail != "max-scale-step" ||
		!strings.Contains(up.Reason, "capped by maxScaleStep 3: wanted 16, applying 4 → 7") {
		t.Errorf("scale-up cap: %+v", up)
	}
	down := enforce(t, 2, from(10, scaleDown(3)))
	if down.TargetReplicas != 8 || down.Guardrail != "max-scale-step" {
		t.Errorf("scale-down cap: %+v", down)
	}
	within := enforce(t, 5, from(4, scaleUp(7)))
	if within.TargetReplicas != 7 || within.Guardrail != "" {
		t.Errorf("a step within the cap must pass untouched: %+v", within)
	}
}

// The cap narrows; it can never push a decision past min/max.
func TestMaxScaleStep_AppliesAfterBounds(t *testing.T) {
	got := enforce(t, 50, from(15, scaleUp(40)))
	if got.TargetReplicas != 20 {
		t.Errorf("bounds still apply under a large step: want 20, got %d", got.TargetReplicas)
	}
}

// Regression: with spec.policy unset, Enforce behaves exactly as before.
func TestPolicyUnset_DecisionsUnchanged(t *testing.T) {
	for _, d := range []reasoning.ScaleDecision{from(4, scaleUp(16)), from(10, scaleDown(3)), scaleUp(30)} {
		got := enforce(t, 0, d)
		want := d.TargetReplicas
		if want > 20 {
			want = 20
		}
		if got.Action != d.Action || got.TargetReplicas != want || got.Guardrail != "" {
			t.Errorf("unset policy changed %+v into %+v", d, got)
		}
	}
}

// A clamp that leaves the count unchanged is a hold that says why, not a
// "scale-up 20 → 20" every cycle.
func TestAtBound_NoOpBecomesExplainedHold(t *testing.T) {
	got := enforce(t, 0, from(20, scaleUp(26)))
	if got.Action != reasoning.ActionHold || got.Guardrail != "replica-bounds" ||
		!strings.Contains(got.Reason, "already at 20 replicas, the most this policy allows") {
		t.Errorf("at max: %+v", got)
	}
	if got.Provider != "" || got.Confidence != 0.9 {
		t.Errorf("hold must keep the original confidence/provider: %+v", got)
	}
}

func TestCooldownHold_KeepsWhatItBlocked(t *testing.T) {
	spec := baseSpec()
	now := time.Now()
	e := newEnforcerWithClock(nil, now)
	last := metav1.NewTime(now.Add(-time.Minute))
	d := from(4, scaleUp(8))
	d.Provider = "anthropic"
	got, err := e.Enforce(context.Background(), spec, &last, d)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != reasoning.ActionHold || got.Guardrail != "cooldown" || got.Provider != "anthropic" ||
		!strings.Contains(got.Reason, "would have scaled to 8 replicas") {
		t.Errorf("cooldown hold must keep its context for the audit: %+v", got)
	}
}

// Seen live on kind: pinned at max during cooldown, the hold read "cooldown …
// would have scaled to 10 replicas" from 10. The bound, not the cooldown, is
// what stops it.
func TestCooldownAtBound_ReportsBound(t *testing.T) {
	spec := baseSpec()
	now := time.Now()
	e := newEnforcerWithClock(nil, now)
	last := metav1.NewTime(now.Add(-time.Minute))
	got, err := e.Enforce(context.Background(), spec, &last, from(20, scaleUp(26)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != reasoning.ActionHold || got.Guardrail != "replica-bounds" ||
		strings.Contains(got.Reason, "would have scaled") {
		t.Errorf("at max in cooldown: %+v", got)
	}
}

func TestGateApproval(t *testing.T) {
	spec := baseSpec()
	spec.Policy.ApprovalAboveReplicas = 10

	held, pending := GateApproval(spec, 0, from(8, scaleUp(14)))
	if !pending || held.Action != reasoning.ActionHold || held.Guardrail != "approval-required" ||
		!strings.Contains(held.Reason, ApprovalAnnotation+"=14") {
		t.Errorf("above threshold without approval: pending=%v %+v", pending, held)
	}
	if got, p := GateApproval(spec, 14, from(8, scaleUp(14))); p || got.TargetReplicas != 14 {
		t.Errorf("approved up to the target must pass: pending=%v %+v", p, got)
	}
	if _, p := GateApproval(spec, 12, from(8, scaleUp(14))); !p {
		t.Error("an approval below the target must not cover it")
	}
	if got, p := GateApproval(spec, 0, from(4, scaleUp(10))); p || got.Action != actionScaleUp {
		t.Errorf("at the threshold needs no approval: pending=%v %+v", p, got)
	}
	if _, p := GateApproval(spec, 0, from(14, scaleDown(4))); p {
		t.Error("scale-downs never need approval")
	}
	spec.Policy.ApprovalAboveReplicas = 0
	if _, p := GateApproval(spec, 0, from(2, scaleUp(20))); p {
		t.Error("gate is off by default")
	}
}
