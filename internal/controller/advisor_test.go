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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/observability"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

// recommendedMetric reads the advisor gauge for one autoscaler in "default" from the
// controller-runtime registry that /metrics serves.
func recommendedMetric(t *testing.T, name string) (float64, bool) {
	t.Helper()
	families, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "agentic_autoscaler_recommended_replicas" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["autoscaler_namespace"] == "default" && labels["autoscaler"] == name {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func makeAdvisorAA(name string) *scalingv1alpha1.AgenticAutoscaler {
	aa := makeAA(name, "default", "my-service")
	aa.Spec.HPACoexistence.Mode = modeAdvisor
	return aa
}

func scaleUpTo(n int32) reasoning.ScaleDecision {
	return reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: n,
		Confidence:     0.9,
		Reason:         "connection pool exhausted with p99 latency spike",
		Timestamp:      newTimestamp(time.Now()),
	}
}

func getAA(t *testing.T, rec *AgenticAutoscalerReconciler, name string) *scalingv1alpha1.AgenticAutoscaler {
	t.Helper()
	aa := &scalingv1alpha1.AgenticAutoscaler{}
	if err := rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, aa); err != nil {
		t.Fatalf("get AA: %v", err)
	}
	return aa
}

func TestReconcile_Advisor_PublishesMetricInsteadOfPatching(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAdvisorAA("advisor-up")
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, makeDeployment("my-service", "default", 3)}, scaleUpTo(8))

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "advisor-up")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	deploy := &appsv1.Deployment{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "my-service"}, deploy)
	if *deploy.Spec.Replicas != 3 {
		t.Errorf("advisor mode must never write spec.replicas: want 3, got %d", *deploy.Spec.Replicas)
	}
	if v, ok := recommendedMetric(t, "advisor-up"); !ok || v != 8 {
		t.Errorf("recommended metric: want 8, got %v (present=%v)", v, ok)
	}

	got := getAA(t, rec, "advisor-up")
	if got.Status.RecommendedReplicas != 8 {
		t.Errorf("status.recommendedReplicas: want 8, got %d", got.Status.RecommendedReplicas)
	}
	if got.Status.CurrentReplicas != 3 {
		t.Errorf("status.currentReplicas: want live 3, got %d", got.Status.CurrentReplicas)
	}
	if got.Status.LastScaleTime == nil {
		t.Error("a changed recommendation must start the cooldown (lastScaleTime set)")
	}
	if got.Status.HPACoexistenceStatus != modeAdvisor {
		t.Errorf("hpaCoexistenceStatus: want advisor, got %q", got.Status.HPACoexistenceStatus)
	}
	if !strings.HasPrefix(got.Status.LastDecisionReason, "[advisor] recommend 8 replicas:") {
		t.Errorf("reason: want advisor prefix, got %q", got.Status.LastDecisionReason)
	}
}

func TestReconcile_Advisor_HoldKeepsPreviousRecommendation(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAdvisorAA("advisor-hold")
	aa.Status.RecommendedReplicas = 6
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, makeDeployment("my-service", "default", 6)},
		reasoning.HoldDecision("signals steady"))

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "advisor-hold")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := getAA(t, rec, "advisor-hold")
	if got.Status.RecommendedReplicas != 6 {
		t.Errorf("hold must keep the recommendation: want 6, got %d", got.Status.RecommendedReplicas)
	}
	if v, _ := recommendedMetric(t, "advisor-hold"); v != 6 {
		t.Errorf("metric: want 6, got %v", v)
	}
	if got.Status.LastScaleTime != nil {
		t.Error("a hold must not start the cooldown")
	}
}

func TestReconcile_Advisor_FirstHoldStartsAtMinReplicas(t *testing.T) {
	s := reconcilerScheme(t)
	rec, _ := makeReconciler(t, s,
		[]runtime.Object{makeAdvisorAA("advisor-first"), makeDeployment("my-service", "default", 5)},
		reasoning.HoldDecision("signals steady"))

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "advisor-first")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// minReplicas (2) leaves the HPA's own metrics in charge until the agent asks for more.
	if v, _ := recommendedMetric(t, "advisor-first"); v != 2 {
		t.Errorf("metric: want minReplicas 2, got %v", v)
	}
}

func TestReconcile_Advisor_DryRunPublishesNothing(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAdvisorAA("advisor-dry")
	aa.Spec.DryRun = true
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, makeDeployment("my-service", "default", 3)}, scaleUpTo(8))

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "advisor-dry")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := recommendedMetric(t, "advisor-dry"); ok {
		t.Error("dry-run advisor must not publish a metric an HPA could act on")
	}
	got := getAA(t, rec, "advisor-dry")
	if got.Status.RecommendedReplicas != 8 || !strings.HasPrefix(got.Status.LastDecisionReason, "[dry-run]") {
		t.Errorf("dry-run should still record what it would publish: got %d, %q",
			got.Status.RecommendedReplicas, got.Status.LastDecisionReason)
	}
}

func TestReconcile_Advisor_MetricClearedOnDeleteAndModeSwitch(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAdvisorAA("advisor-gone")
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, makeDeployment("my-service", "default", 3)}, scaleUpTo(5))
	ctx := context.Background()

	if _, err := rec.Reconcile(ctx, reconcileRequest("default", "advisor-gone")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := recommendedMetric(t, "advisor-gone"); !ok {
		t.Fatal("expected a published recommendation before the switch")
	}

	// Switching to owner mode must stop publishing.
	live := getAA(t, rec, "advisor-gone")
	live.Spec.HPACoexistence.Mode = "owner"
	live.Status.LastScaleTime = nil
	if err := rec.Update(ctx, live); err != nil {
		t.Fatalf("update mode: %v", err)
	}
	if _, err := rec.Reconcile(ctx, reconcileRequest("default", "advisor-gone")); err != nil {
		t.Fatalf("Reconcile owner: %v", err)
	}
	if _, ok := recommendedMetric(t, "advisor-gone"); ok {
		t.Error("metric must be cleared after leaving advisor mode")
	}

	// Back to advisor, then delete the CR: the series must disappear.
	live = getAA(t, rec, "advisor-gone")
	live.Spec.HPACoexistence.Mode = modeAdvisor
	if err := rec.Update(ctx, live); err != nil {
		t.Fatalf("update mode: %v", err)
	}
	if _, err := rec.Reconcile(ctx, reconcileRequest("default", "advisor-gone")); err != nil {
		t.Fatalf("Reconcile advisor: %v", err)
	}
	if _, ok := recommendedMetric(t, "advisor-gone"); !ok {
		t.Fatal("expected a published recommendation before the delete")
	}
	if err := rec.Delete(ctx, getAA(t, rec, "advisor-gone")); err != nil {
		t.Fatalf("delete AA: %v", err)
	}
	if _, err := rec.Reconcile(ctx, reconcileRequest("default", "advisor-gone")); err != nil {
		t.Fatalf("Reconcile after delete: %v", err)
	}
	if _, ok := recommendedMetric(t, "advisor-gone"); ok {
		t.Error("metric must be cleared when the CR is deleted")
	}
}

func TestReconcile_Advisor_AnnotatesOnlyChangedRecommendations(t *testing.T) {
	var annotations atomic.Int32
	grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		annotations.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer grafana.Close()

	s := reconcilerScheme(t)
	aa := makeAdvisorAA("advisor-annotate")
	aa.Spec.CooldownSeconds = 0
	// The HPA is still holding 10 pods, so the agent keeps asking for 2.
	scaleDown := reasoning.ScaleDecision{Action: "scale-down", TargetReplicas: 2, Confidence: 0.9,
		Reason: "idle", Timestamp: newTimestamp(time.Now())}
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, makeDeployment("my-service", "default", 10)}, scaleDown)
	rec.Grafana = observability.NewGrafanaClient(observability.GrafanaConfig{URL: grafana.URL})

	for i := 0; i < 3; i++ {
		if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "advisor-annotate")); err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
	}
	// prev=0 → 2 is the only change; the two repeats must not annotate.
	if n := annotations.Load(); n != 1 {
		t.Errorf("annotations: want 1 for one changed recommendation, got %d", n)
	}
}

func TestAdvisorRecommendation_ClampsToSpecBounds(t *testing.T) {
	spec := scalingv1alpha1.AgenticAutoscalerSpec{MinReplicas: 2, MaxReplicas: 10}
	cases := []struct {
		name string
		prev int32
		d    reasoning.ScaleDecision
		want int32
	}{
		{"unset prev on hold starts at min", 0, reasoning.HoldDecision("x"), 2},
		{"hold keeps prev", 7, reasoning.HoldDecision("x"), 7},
		{"scale-up takes target", 3, scaleUpTo(6), 6},
		{"target above max is clamped", 3, scaleUpTo(40), 10},
		{"scale-down below min is clamped", 8, reasoning.ScaleDecision{Action: "scale-down", TargetReplicas: 1}, 2},
	}
	for _, tc := range cases {
		if got := advisorRecommendation(tc.prev, spec, tc.d); got != tc.want {
			t.Errorf("%s: want %d, got %d", tc.name, tc.want, got)
		}
	}
}
