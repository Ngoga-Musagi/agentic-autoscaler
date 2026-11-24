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

// Package policy applies safety constraints to ScaleDecisions before they
// reach the scaler. It enforces CR spec replica bounds, cooldown windows,
// and — in calibrated coexistence mode — the bounds of a live HPA.
//
// IMPORTANT: this package never modifies or deletes HPA objects. All HPA
// access is read-only via the HPAReader interface. See .claude/rules/hpa-coexistence.md.
package policy

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

const (
	actionScaleUp         = "scale-up"
	guardrailMaxScaleStep = "max-scale-step"
)

// HPAReader fetches a HorizontalPodAutoscaler by namespace and name.
// The Enforcer uses this read-only in "calibrated" coexistence mode.
// Implementations must never modify or delete HPA objects.
type HPAReader interface {
	GetHPA(ctx context.Context, namespace, name string) (*autoscalingv2.HorizontalPodAutoscaler, error)
}

// Enforcer applies policy constraints to a ScaleDecision before it reaches
// the scaler. Constraints are applied in order:
//  1. Hold decisions pass through unchanged (no bounds or cooldown apply).
//  2. Cooldown: if the last scale action was within CooldownSeconds, return hold.
//  3. Clamp TargetReplicas to [spec.MinReplicas, spec.MaxReplicas].
//  4. Calibrated coexistence: additionally clamp within the live HPA's bounds.
//  5. spec.policy.maxScaleStep: cap how far one decision may move from the
//     current replica count (after the bounds, so it can only narrow them).
//
// The approval gate (GateApproval) runs after Enforce, in the controller,
// because approval lives in an annotation rather than in the spec.
type Enforcer struct {
	cooldown  *CooldownChecker
	hpaReader HPAReader
	// now returns the current time. Overridden in tests to avoid real sleeps.
	now func() time.Time
}

// NewEnforcer returns an Enforcer. hpaReader may be nil when HPACoexistence
// is not configured; it is required when mode is "calibrated".
func NewEnforcer(hpaReader HPAReader) *Enforcer {
	return &Enforcer{
		cooldown:  &CooldownChecker{},
		hpaReader: hpaReader,
		now:       time.Now,
	}
}

// Enforce applies all policy rules to decision and returns the (possibly
// modified) result. lastScaleTime is sourced from AgenticAutoscalerStatus.LastScaleTime.
func (e *Enforcer) Enforce(
	ctx context.Context,
	spec v1alpha1.AgenticAutoscalerSpec,
	lastScaleTime *metav1.Time,
	decision reasoning.ScaleDecision,
) (reasoning.ScaleDecision, error) {
	// Hold decisions carry no TargetReplicas — pass straight through.
	if decision.Action == reasoning.ActionHold {
		return decision, nil
	}

	// Cooldown blocks all non-hold scale actions.
	if e.cooldown.IsActive(lastScaleTime, spec.CooldownSeconds, e.now()) {
		// Already pinned at a CR bound: say that, not "would have scaled to 10"
		// from 10 — the cooldown is not what stops it.
		clamped := e.clampToCRBounds(decision, spec.MinReplicas, spec.MaxReplicas)
		if b := noOpAtBound(clamped); b.Action == reasoning.ActionHold {
			return b, nil
		}
		log.Printf("INFO: policy: cooldown active (%ds window), converting %s to hold",
			spec.CooldownSeconds, decision.Action)
		return reasoning.HeldBy(decision, "cooldown",
			fmt.Sprintf("cooldown active (%ds window)", spec.CooldownSeconds)), nil
	}

	// Clamp to the bounds declared in the CR spec.
	decision = e.clampToCRBounds(decision, spec.MinReplicas, spec.MaxReplicas)

	// Calibrated coexistence: further clamp within the live HPA's bounds.
	// Per hpa-coexistence.md: we read-only, never modify or delete the HPA.
	if spec.HPACoexistence.Mode == "calibrated" {
		if e.hpaReader == nil {
			return reasoning.ScaleDecision{}, fmt.Errorf(
				"calibrated coexistence mode requires an HPAReader but none was provided")
		}
		var err error
		decision, err = e.clampToHPABounds(ctx, spec, decision)
		if err != nil {
			return reasoning.ScaleDecision{}, fmt.Errorf("calibrated hpa bounds: %w", err)
		}
	}

	return noOpAtBound(capStep(decision, spec.Policy.MaxScaleStep)), nil
}

// noOpAtBound turns a scale action that clamping reduced to the current count
// into a hold that says so. Without it a service pinned at maxReplicas logs a
// "scale-up 10 → 10" every cycle, which reads as action when nothing happens.
func noOpAtBound(d reasoning.ScaleDecision) reasoning.ScaleDecision {
	if d.Action == reasoning.ActionHold || d.OldReplicas <= 0 || d.TargetReplicas != d.OldReplicas {
		return d
	}
	limit := "fewest"
	if d.Action == actionScaleUp {
		limit = "most"
	}
	h := reasoning.HeldBy(d, "replica-bounds",
		fmt.Sprintf("held: already at %d replicas, the %s this policy allows", d.OldReplicas, limit))
	h.Reason = strings.Replace(h.Reason, fmt.Sprintf("; would have scaled to %d replicas", d.TargetReplicas), "", 1)
	return h
}

// capStep limits the distance from the current replica count to maxStep. It
// needs the current count (decision.OldReplicas, stamped by the controller) and
// does nothing when either is unset.
func capStep(decision reasoning.ScaleDecision, maxStep int32) reasoning.ScaleDecision {
	cur := decision.OldReplicas
	if maxStep <= 0 || cur <= 0 {
		return decision
	}
	wanted := decision.TargetReplicas
	switch {
	case wanted > cur+maxStep:
		decision.TargetReplicas = cur + maxStep
	case wanted < cur-maxStep:
		decision.TargetReplicas = cur - maxStep
	default:
		return decision
	}
	decision.Guardrail = guardrailMaxScaleStep
	decision.Reason = fmt.Sprintf("%s [capped by maxScaleStep %d: wanted %d, applying %d → %d]",
		decision.Reason, maxStep, wanted, cur, decision.TargetReplicas)
	log.Printf("INFO: policy: maxScaleStep %d capped targetReplicas %d → %d", maxStep, wanted, decision.TargetReplicas)
	return decision
}

// ApprovalAnnotation is the CR annotation a human sets to approve scale-ups up
// to a replica count when spec.policy.approvalAboveReplicas is configured.
const ApprovalAnnotation = "agentic-autoscaler.io/approved-replicas"

// GateApproval holds a scale-up whose target exceeds
// spec.policy.approvalAboveReplicas unless approved (the parsed
// ApprovalAnnotation, 0 when absent) covers it. It reports whether the
// decision is now pending approval. Scale-downs and holds pass through.
func GateApproval(
	spec v1alpha1.AgenticAutoscalerSpec, approved int32, d reasoning.ScaleDecision,
) (reasoning.ScaleDecision, bool) {
	limit := spec.Policy.ApprovalAboveReplicas
	if limit <= 0 || d.Action != actionScaleUp || d.TargetReplicas <= limit || approved >= d.TargetReplicas {
		return d, false
	}
	return reasoning.HeldBy(d, "approval-required", fmt.Sprintf(
		"awaiting approval: %d replicas exceeds approvalAboveReplicas %d — set annotation %s=%d to allow it",
		d.TargetReplicas, limit, ApprovalAnnotation, d.TargetReplicas)), true
}

// clampToCRBounds clamps TargetReplicas to [min, max] and logs a warning if
// the value changed.
func (e *Enforcer) clampToCRBounds(decision reasoning.ScaleDecision, min, max int32) reasoning.ScaleDecision {
	original := decision.TargetReplicas
	if decision.TargetReplicas < min {
		decision.TargetReplicas = min
	}
	if decision.TargetReplicas > max {
		decision.TargetReplicas = max
	}
	if decision.TargetReplicas != original {
		log.Printf("WARNING: policy: clamped targetReplicas %d → %d (CR bounds [%d, %d])",
			original, decision.TargetReplicas, min, max)
	}
	return decision
}

// clampToHPABounds fetches the named HPA (read-only) and clamps TargetReplicas
// within [hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas]. Logs a warning on clamp.
func (e *Enforcer) clampToHPABounds(
	ctx context.Context,
	spec v1alpha1.AgenticAutoscalerSpec,
	decision reasoning.ScaleDecision,
) (reasoning.ScaleDecision, error) {
	ns := spec.HPACoexistence.HPANamespace
	if ns == "" {
		ns = spec.Namespace
	}
	name := spec.HPACoexistence.HPAName

	hpa, err := e.hpaReader.GetHPA(ctx, ns, name)
	if err != nil {
		return reasoning.ScaleDecision{}, fmt.Errorf("get hpa %s/%s: %w", ns, name, err)
	}

	original := decision.TargetReplicas

	if decision.TargetReplicas > hpa.Spec.MaxReplicas {
		decision.TargetReplicas = hpa.Spec.MaxReplicas
		log.Printf("WARNING: policy: clamped targetReplicas %d → %d (HPA %s/%s maxReplicas)",
			original, decision.TargetReplicas, ns, name)
	}
	if hpa.Spec.MinReplicas != nil && decision.TargetReplicas < *hpa.Spec.MinReplicas {
		decision.TargetReplicas = *hpa.Spec.MinReplicas
		log.Printf("WARNING: policy: clamped targetReplicas %d → %d (HPA %s/%s minReplicas)",
			original, decision.TargetReplicas, ns, name)
	}

	return decision, nil
}
