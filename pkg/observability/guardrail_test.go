package observability

import (
	"encoding/json"
	"strings"
	"testing"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

// A guardrail outcome must survive into the audit record (and so into the
// console and the Loki decision log), and round-trip through JSON.
func TestDecisionRecord_CarriesGuardrail(t *testing.T) {
	aa := scalingv1alpha1.AgenticAutoscaler{Spec: scalingv1alpha1.AgenticAutoscalerSpec{TargetDeployment: "agent-runner"}}
	held := reasoning.HeldBy(reasoning.ScaleDecision{Action: "scale-up", TargetReplicas: 9, OldReplicas: 3,
		Provider: "anthropic", Reason: "tasks queueing", PatternsMatched: []string{"llm-rate-limited"}},
		"llm-rate-limit", "held: provider rate limiting")

	rec := NewDecisionRecord(aa, held)
	if rec.Guardrail != "llm-rate-limit" || rec.Provider != "anthropic" || rec.NewReplicas != 3 ||
		len(rec.PatternsMatched) != 1 {
		t.Fatalf("record lost guardrail context: %+v", rec)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"guardrail":"llm-rate-limit"`) {
		t.Errorf("guardrail missing from JSON: %s", raw)
	}
	var back DecisionRecord
	if err := json.Unmarshal(raw, &back); err != nil || back.Guardrail != "llm-rate-limit" {
		t.Errorf("round trip: %+v (%v)", back, err)
	}

	plain, _ := json.Marshal(NewDecisionRecord(aa, reasoning.HoldDecision("steady")))
	if strings.Contains(string(plain), "guardrail") {
		t.Errorf("an unguarded decision must not carry a guardrail field: %s", plain)
	}
}
