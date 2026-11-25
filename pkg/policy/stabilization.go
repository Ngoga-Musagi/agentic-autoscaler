package policy

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

const (
	actionScaleDown            = "scale-down"
	guardrailDownStabilization = "scale-down-stabilization"
)

// StabilizeScaleDown holds a scale-down that comes within
// spec.policy.scaleDownStabilizationSeconds of the last scale-up. It runs after
// Enforce, so the decision is already within bounds. Motivated by a kind run
// with an LLM: once 11 agent runners had drained the queue, the model read the
// empty queue as over-provisioning and went 11 → 2 mid-surge. The window delays
// that scale-down by its length; it does not stop it once the window passes
// (re-run on kind: held twice, then 12 → 2), so it is a damper, not a capacity
// floor.
//
// It never holds a scale-down that brings replicas back under a lowered
// maxReplicas: that is the user's explicit instruction, not a judgement on load.
func StabilizeScaleDown(
	spec v1alpha1.AgenticAutoscalerSpec, lastScaleUp *metav1.Time, now time.Time, d reasoning.ScaleDecision,
) reasoning.ScaleDecision {
	window := time.Duration(spec.Policy.ScaleDownStabilizationSeconds) * time.Second
	if window <= 0 || d.Action != actionScaleDown || lastScaleUp == nil {
		return d
	}
	if d.OldReplicas > spec.MaxReplicas {
		return d
	}
	since := now.Sub(lastScaleUp.Time)
	if since < 0 || since >= window {
		return d
	}
	return reasoning.HeldBy(d, guardrailDownStabilization, fmt.Sprintf(
		"held: scaled up %s ago; scale-downs wait %ds (scaleDownStabilizationSeconds) "+
			"so a queue the new replicas just drained is not read as spare capacity",
		since.Round(time.Second), spec.Policy.ScaleDownStabilizationSeconds))
}
