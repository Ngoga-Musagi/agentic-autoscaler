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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/policy"
)

// replicasOf reads spec.replicas of the "my-service" Deployment the tests target.
func replicasOf(t *testing.T, rec *AgenticAutoscalerReconciler) int32 {
	t.Helper()
	d := &appsv1.Deployment{}
	if err := rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "my-service"}, d); err != nil {
		t.Fatal(err)
	}
	return *d.Spec.Replicas
}

func TestReconcile_PauseForcesHold(t *testing.T) {
	cases := map[string]func(aaAnn, depAnn map[string]string, t *testing.T){
		"CR annotation":         func(aa, _ map[string]string, _ *testing.T) { aa[PauseAnnotation] = "true" },
		"Deployment annotation": func(_, dep map[string]string, _ *testing.T) { dep[PauseAnnotation] = "true" },
		"global kill switch":    func(_, _ map[string]string, t *testing.T) { t.Setenv(pauseAllEnv, "true") },
	}
	for name, pause := range cases {
		t.Run(name, func(t *testing.T) {
			s := reconcilerScheme(t)
			aa := makeAA("paused", "default", "my-service")
			aa.Annotations = map[string]string{}
			deploy := makeDeployment("my-service", "default", 3)
			deploy.Annotations = map[string]string{}
			pause(aa.Annotations, deploy.Annotations, t)

			rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, scaleUpTo(8))
			if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "paused")); err != nil {
				t.Fatal(err)
			}
			if r := replicasOf(t, rec); r != 3 {
				t.Errorf("paused autoscaler scaled to %d", r)
			}
			got := getAA(t, rec, "paused")
			if got.Status.LastGuardrail != "paused" || !strings.Contains(got.Status.LastDecisionReason, "paused by") ||
				!strings.Contains(got.Status.LastDecisionReason, "would have scaled to 8") {
				t.Errorf("status must show the pause and what it blocked: %q / %q",
					got.Status.LastGuardrail, got.Status.LastDecisionReason)
			}
		})
	}
}

func TestReconcile_ApprovalGate(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("gated", "default", "my-service")
	aa.Spec.CooldownSeconds = 0
	aa.Spec.Policy.ApprovalAboveReplicas = 6
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, makeDeployment("my-service", "default", 3)}, scaleUpTo(9))
	ctx := context.Background()

	if _, err := rec.Reconcile(ctx, reconcileRequest("default", "gated")); err != nil {
		t.Fatal(err)
	}
	if r := replicasOf(t, rec); r != 3 {
		t.Fatalf("unapproved scale-up above the threshold was applied: %d", r)
	}
	got := getAA(t, rec, "gated")
	if got.Status.PendingApproval == nil || got.Status.PendingApproval.TargetReplicas != 9 ||
		got.Status.LastGuardrail != "approval-required" {
		t.Fatalf("pending approval not recorded: %+v / %q", got.Status.PendingApproval, got.Status.LastGuardrail)
	}

	// A human approves up to 9 → the next reconcile applies it and clears the request.
	got.Annotations = map[string]string{policy.ApprovalAnnotation: "9"}
	if err := rec.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Reconcile(ctx, reconcileRequest("default", "gated")); err != nil {
		t.Fatal(err)
	}
	if r := replicasOf(t, rec); r != 9 {
		t.Errorf("approved scale-up not applied: %d", r)
	}
	if after := getAA(t, rec, "gated"); after.Status.PendingApproval != nil {
		t.Errorf("pending approval must clear once applied: %+v", after.Status.PendingApproval)
	}
}

func TestReconcile_ConfidenceFloorFallsBackToRules(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("floor", "default", "my-service")
	aa.Spec.Policy.MinConfidence = 70
	weak := scaleUpTo(12)
	weak.Confidence = 0.3
	weak.Provider = "anthropic"
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, makeDeployment("my-service", "default", 3)}, weak)

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "floor")); err != nil {
		t.Fatal(err)
	}
	// The static collector reports healthy signals, so the rules hold.
	if r := replicasOf(t, rec); r != 3 {
		t.Errorf("low-confidence AI decision was applied: %d", r)
	}
	got := getAA(t, rec, "floor")
	if got.Status.LastGuardrail != "min-confidence" || !strings.Contains(got.Status.LastDecisionReason, "below the 0.70 floor") {
		t.Errorf("status must explain the fallback: %q / %q", got.Status.LastGuardrail, got.Status.LastDecisionReason)
	}
}
