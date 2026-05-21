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
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
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
	if decision.Action == "hold" {
		return decision, nil
	}

	// Cooldown blocks all non-hold scale actions.
	if e.cooldown.IsActive(lastScaleTime, spec.CooldownSeconds, e.now()) {
		log.Printf("INFO: policy: cooldown active (%ds window), converting %s to hold",
			spec.CooldownSeconds, decision.Action)
		return reasoning.HoldDecision("cooldown active: skipping scale action until window expires"), nil
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

	return decision, nil
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
