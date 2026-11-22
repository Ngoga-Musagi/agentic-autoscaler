package reasoning

import (
	"context"
	"strings"
	"testing"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

type stubAgent struct{ d ScaleDecision }

func (s stubAgent) Decide(context.Context, fusion.FusedSignal) (ScaleDecision, error) {
	return s.d, nil
}

func TestConfidenceFloor(t *testing.T) {
	rules := NewRuleBasedAgent(baseConfig)
	sig := makeSignal(signals.MetricSnapshot{ErrorRatePct: 25}) // rule 3 → scale-up to 7

	weak := stubAgent{ScaleDecision{Action: "scale-up", TargetReplicas: 18, Confidence: 0.41, Provider: "anthropic"}}
	got, err := WithConfidenceFloor(weak, rules, 0.7).Decide(context.Background(), sig)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "rule-based" || got.TargetReplicas != 7 || got.Guardrail != "min-confidence" ||
		!strings.Contains(got.Reason, "anthropic confidence 0.41 is below the 0.70 floor") {
		t.Errorf("below the floor the rules must decide: %+v", got)
	}

	strong := stubAgent{ScaleDecision{Action: "scale-up", TargetReplicas: 9, Confidence: 0.9, Provider: "anthropic"}}
	got, _ = WithConfidenceFloor(strong, rules, 0.7).Decide(context.Background(), sig)
	if got.TargetReplicas != 9 || got.Guardrail != "" {
		t.Errorf("at or above the floor the AI decision stands: %+v", got)
	}

	// The floor checks the model, not the rules (which fall back on themselves).
	fallback := stubAgent{ScaleDecision{Action: "hold", Confidence: 0, Provider: "rule-based"}}
	if got, _ := WithConfidenceFloor(fallback, rules, 0.7).Decide(context.Background(), sig); got.Action != "hold" {
		t.Errorf("rule-based decisions pass through: %+v", got)
	}
}
