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
	"encoding/json"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/observability"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/policy"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/scaler"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

// ---- fakes -----------------------------------------------------------------

// staticSignalCollector always returns an empty, non-error snapshot.
type staticSignalCollector struct {
	snapshot signals.SystemSnapshot
	err      error
}

func (s *staticSignalCollector) Collect(_ context.Context, _ scalingv1alpha1.AgenticAutoscalerSpec) (signals.SystemSnapshot, error) {
	return s.snapshot, s.err
}

// fixedAgent always returns the provided decision.
type fixedAgent struct {
	decision reasoning.ScaleDecision
	err      error
}

func (f *fixedAgent) Decide(_ context.Context, _ fusion.FusedSignal) (reasoning.ScaleDecision, error) {
	return f.decision, f.err
}

// noopHPAReader satisfies policy.HPAReader; returns not-found for all HPAs.
type noopHPAReader struct{}

func (n *noopHPAReader) GetHPA(_ context.Context, _, _ string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	return nil, nil
}

// ---- scheme & builders -----------------------------------------------------

func reconcilerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := scalingv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newTimestamp(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

// makeReconciler constructs a full reconciler with a given agent decision.
func makeReconciler(
	t *testing.T,
	scheme *runtime.Scheme,
	objs []runtime.Object,
	decision reasoning.ScaleDecision,
) (*AgenticAutoscalerReconciler, *fake.ClientBuilder) {
	t.Helper()

	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{})

	rObjs := make([]runtime.Object, len(objs))
	copy(rObjs, objs)

	fc := builder.WithRuntimeObjects(rObjs...).Build()

	rec := &AgenticAutoscalerReconciler{
		Client:         fc,
		Scheme:         scheme,
		SignalCollector: &staticSignalCollector{},
		Policy:         policy.NewEnforcer(&noopHPAReader{}),
		Scaler:         scaler.NewExecutor(fc),
		Observability:  observability.NewNoopRecorder(),
		NewAgent: func(_ reasoning.Config) reasoning.Agent {
			return &fixedAgent{decision: decision}
		},
	}
	return rec, builder
}

// ---- helpers ---------------------------------------------------------------

func makeAA(name, ns, targetDeploy string) *scalingv1alpha1.AgenticAutoscaler {
	return &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: scalingv1alpha1.AgenticAutoscalerSpec{
			TargetDeployment: targetDeploy,
			Namespace:        ns,
			PrometheusURL:    "http://prometheus:9090",
			LogSource:        scalingv1alpha1.LogSourceConfig{Type: "loki"},
			MinReplicas:      2,
			MaxReplicas:      20,
			CooldownSeconds:  60,
		},
	}
}

func makeDeployment(name, ns string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
}

func reconcileRequest(ns, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

// ---- tests -----------------------------------------------------------------

func TestReconcile_HoldDecision_StatusUpdated(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	deploy := makeDeployment("my-service", "default", 5)

	holdDecision := reasoning.HoldDecision("steady state, no action needed")
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, holdDecision)

	result, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter < 30*time.Second {
		t.Errorf("RequeueAfter: want >= 30s, got %s", result.RequeueAfter)
	}

	updated := &scalingv1alpha1.AgenticAutoscaler{}
	if err := rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "autoscaler"}, updated); err != nil {
		t.Fatalf("Get AA after reconcile: %v", err)
	}
	if updated.Status.LastDecisionReason != holdDecision.Reason {
		t.Errorf("LastDecisionReason: want %q, got %q", holdDecision.Reason, updated.Status.LastDecisionReason)
	}
	if updated.Status.CurrentReplicas != 5 {
		t.Errorf("CurrentReplicas: want 5, got %d", updated.Status.CurrentReplicas)
	}
}

func TestReconcile_ScaleUpDecision_PatchesDeployment(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	deploy := makeDeployment("my-service", "default", 3)

	ts := newTimestamp(time.Now())
	scaleUp := reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: 8,
		Confidence:     0.9,
		Reason:         "high load detected",
		Timestamp:      ts,
	}
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, scaleUp)

	_, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Deployment replicas must be updated.
	updatedDeploy := &appsv1.Deployment{}
	if err := rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "my-service"}, updatedDeploy); err != nil {
		t.Fatalf("Get Deployment: %v", err)
	}
	if updatedDeploy.Spec.Replicas == nil || *updatedDeploy.Spec.Replicas != 8 {
		t.Errorf("Spec.Replicas: want 8, got %v", updatedDeploy.Spec.Replicas)
	}

	// Status must reflect the scale action.
	updatedAA := &scalingv1alpha1.AgenticAutoscaler{}
	if err := rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "autoscaler"}, updatedAA); err != nil {
		t.Fatalf("Get AA: %v", err)
	}
	if updatedAA.Status.CurrentReplicas != 8 {
		t.Errorf("Status.CurrentReplicas: want 8, got %d", updatedAA.Status.CurrentReplicas)
	}
	if updatedAA.Status.LastDecisionReason != "high load detected" {
		t.Errorf("Status.LastDecisionReason: want %q, got %q", "high load detected", updatedAA.Status.LastDecisionReason)
	}
}

func TestReconcile_DryRun_DoesNotPatchDeployment(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	aa.Spec.DryRun = true
	deploy := makeDeployment("my-service", "default", 3)

	scaleUp := reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: 10,
		Confidence:     0.9,
		Reason:         "would scale up in dry-run",
		Timestamp:      newTimestamp(time.Now()),
	}
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, scaleUp)

	_, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile dry-run: %v", err)
	}

	// Deployment must be unchanged.
	updatedDeploy := &appsv1.Deployment{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "my-service"}, updatedDeploy)
	if updatedDeploy.Spec.Replicas == nil || *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Spec.Replicas: want 3 (unchanged, dry-run), got %v", updatedDeploy.Spec.Replicas)
	}

	// Decision reason must still be recorded in status.
	updatedAA := &scalingv1alpha1.AgenticAutoscaler{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "autoscaler"}, updatedAA)
	if updatedAA.Status.LastDecisionReason == "" {
		t.Error("Status.LastDecisionReason: want non-empty in dry-run mode")
	}
}

func TestReconcile_CRNotFound_ReturnsNil(t *testing.T) {
	s := reconcilerScheme(t)
	fc := fake.NewClientBuilder().WithScheme(s).Build()

	rec := &AgenticAutoscalerReconciler{
		Client:         fc,
		Scheme:         s,
		SignalCollector: &staticSignalCollector{},
		Policy:         policy.NewEnforcer(&noopHPAReader{}),
		Scaler:         scaler.NewExecutor(fc),
		Observability:  observability.NewNoopRecorder(),
		NewAgent: func(_ reasoning.Config) reasoning.Agent {
			return &fixedAgent{decision: reasoning.HoldDecision("hold")}
		},
	}

	result, err := rec.Reconcile(context.Background(), reconcileRequest("default", "nonexistent"))
	if err != nil {
		t.Fatalf("Reconcile: want nil for missing CR, got %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("Result: want empty for missing CR, got %+v", result)
	}
}

func TestReconcile_DeploymentNotFound_RequeusWith30s(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "missing-deploy")

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{}).
		WithRuntimeObjects(aa).
		Build()

	rec := &AgenticAutoscalerReconciler{
		Client:         fc,
		Scheme:         s,
		SignalCollector: &staticSignalCollector{},
		Policy:         policy.NewEnforcer(&noopHPAReader{}),
		Scaler:         scaler.NewExecutor(fc),
		Observability:  observability.NewNoopRecorder(),
		NewAgent: func(_ reasoning.Config) reasoning.Agent {
			return &fixedAgent{decision: reasoning.HoldDecision("hold")}
		},
	}

	result, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: want nil for missing Deployment, got %v", err)
	}
	if result.RequeueAfter < 30*time.Second {
		t.Errorf("RequeueAfter: want >= 30s backoff, got %s", result.RequeueAfter)
	}
}

func TestReconcile_SignalCollectionFailure_RequeusWith30s(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	deploy := makeDeployment("my-service", "default", 3)

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{}).
		WithRuntimeObjects(aa, deploy).
		Build()

	rec := &AgenticAutoscalerReconciler{
		Client:         fc,
		Scheme:         s,
		SignalCollector: &staticSignalCollector{err: &fakeCollectorError{}},
		Policy:         policy.NewEnforcer(&noopHPAReader{}),
		Scaler:         scaler.NewExecutor(fc),
		Observability:  observability.NewNoopRecorder(),
		NewAgent: func(_ reasoning.Config) reasoning.Agent {
			return &fixedAgent{decision: reasoning.HoldDecision("hold")}
		},
	}

	result, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: want nil for signal failure (backoff), got %v", err)
	}
	if result.RequeueAfter < 30*time.Second {
		t.Errorf("RequeueAfter: want >= 30s, got %s", result.RequeueAfter)
	}
}

// fakeCollectorError is a simple error value for the signal collector.
type fakeCollectorError struct{}

func (e *fakeCollectorError) Error() string { return "prometheus: connection refused" }

func TestReconcile_CooldownActive_DecisionBecomesHold(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	// Last scale happened 10 seconds ago; cooldown is 60s → still active.
	recentScale := metav1.NewTime(time.Now().Add(-10 * time.Second))
	aa.Status.LastScaleTime = &recentScale
	deploy := makeDeployment("my-service", "default", 5)

	scaleUp := reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: 10,
		Confidence:     0.9,
		Reason:         "high load",
		Timestamp:      newTimestamp(time.Now()),
	}

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{}).
		WithRuntimeObjects(aa, deploy).
		Build()

	rec := &AgenticAutoscalerReconciler{
		Client:         fc,
		Scheme:         s,
		SignalCollector: &staticSignalCollector{},
		Policy:         policy.NewEnforcer(&noopHPAReader{}),
		Scaler:         scaler.NewExecutor(fc),
		Observability:  observability.NewNoopRecorder(),
		NewAgent: func(_ reasoning.Config) reasoning.Agent {
			return &fixedAgent{decision: scaleUp}
		},
	}

	_, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Deployment replicas must be unchanged because cooldown is active.
	updatedDeploy := &appsv1.Deployment{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "my-service"}, updatedDeploy)
	if updatedDeploy.Spec.Replicas == nil || *updatedDeploy.Spec.Replicas != 5 {
		t.Errorf("Spec.Replicas: want 5 (cooldown active, no scale), got %v", updatedDeploy.Spec.Replicas)
	}
}

func TestReconcile_HPACoexistenceStatus_OwnerMode(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	aa.Spec.HPACoexistence = scalingv1alpha1.HPACoexistence{Mode: "owner"}
	deploy := makeDeployment("my-service", "default", 5)

	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, reasoning.HoldDecision("hold"))

	_, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &scalingv1alpha1.AgenticAutoscaler{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "autoscaler"}, updated)
	if updated.Status.HPACoexistenceStatus != "owner" {
		t.Errorf("HPACoexistenceStatus: want %q, got %q", "owner", updated.Status.HPACoexistenceStatus)
	}
}

func TestReconcile_RequeueAfterRespectsCooldownSeconds(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	aa.Spec.CooldownSeconds = 120
	deploy := makeDeployment("my-service", "default", 5)

	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, reasoning.HoldDecision("hold"))

	result, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter != 120*time.Second {
		t.Errorf("RequeueAfter: want 120s, got %s", result.RequeueAfter)
	}
}

func TestReconcile_ObservedGenerationUpdated(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	deploy := makeDeployment("my-service", "default", 5)

	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, reasoning.HoldDecision("hold"))

	_, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &scalingv1alpha1.AgenticAutoscaler{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "autoscaler"}, updated)
	// ObservedGeneration should match the CR's generation (fake client sets it to 0).
	if updated.Status.ObservedGeneration != aa.Generation {
		t.Errorf("ObservedGeneration: want %d, got %d", aa.Generation, updated.Status.ObservedGeneration)
	}
}

// TestReconcile_AuditConfigMapCreated verifies the end-to-end wiring:
// ConfigMapRecorder is called on every reconcile (including hold/dry-run) and
// creates the decision-audit-log ConfigMap in the operator namespace.
func TestReconcile_AuditConfigMapCreated(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", "operator-ns")

	s := reconcilerScheme(t)

	aa := makeAA("autoscaler", "default", "api")
	aa.Spec.DryRun = true // dry-run: Record is still expected
	deploy := makeDeployment("api", "default", 3)

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{}).
		WithRuntimeObjects(aa, deploy).
		Build()

	ts := metav1.NewTime(time.Now())
	decision := reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: 6,
		Confidence:     0.9,
		Reason:         "high load",
		Timestamp:      &ts,
		Provider:       "rule-based",
	}

	rec := &AgenticAutoscalerReconciler{
		Client:          fc,
		Scheme:          s,
		SignalCollector:  &staticSignalCollector{},
		Policy:          policy.NewEnforcer(&noopHPAReader{}),
		Scaler:          scaler.NewExecutor(fc),
		Observability:   observability.NewConfigMapRecorder(fc),
		NewAgent: func(_ reasoning.Config) reasoning.Agent {
			return &fixedAgent{decision: decision}
		},
	}

	_, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The audit ConfigMap must exist in the operator namespace.
	cm := &corev1.ConfigMap{}
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "decision-audit-log", Namespace: "operator-ns"},
		cm); err != nil {
		t.Fatalf("audit ConfigMap not created: %v", err)
	}

	raw := cm.Data["decisions.json"]
	if raw == "" {
		t.Fatal("decisions.json is empty")
	}

	var entries []observability.DecisionRecord
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("unmarshal decisions.json: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries: want 1, got %d", len(entries))
	}

	e := entries[0]
	checks := []struct {
		field string
		ok    bool
	}{
		{"Action", e.Action == "scale-up"},
		{"Deployment", e.Deployment == "api"},
		{"OldReplicas", e.OldReplicas == 3},
		{"NewReplicas", e.NewReplicas == 6},
		{"Provider", e.Provider == "rule-based"},
		{"DryRun", e.DryRun},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("DecisionRecord.%s: assertion failed (got %+v)", c.field, e)
		}
	}
	t.Logf("audit ConfigMap verified: %s", raw)
}

func TestReconcile_GrafanaNotCalledOnHold(t *testing.T) {
	// Verify that a nil Grafana field (the default in tests) is safe for hold decisions.
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "api")
	aa.Spec.DryRun = false
	deploy := makeDeployment("api", "default", 3)

	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, reasoning.HoldDecision("steady state"))
	// Grafana is nil (not set in makeReconciler) — must not panic.

	_, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile with nil Grafana and hold: %v", err)
	}
}

func TestReconcile_DeploymentNamespaceFallsBackToCRNamespace(t *testing.T) {
	s := reconcilerScheme(t)
	// Namespace in spec is empty — should fall back to the CR's namespace "production".
	aa := makeAA("autoscaler", "production", "api")
	aa.Spec.Namespace = ""
	deploy := makeDeployment("api", "production", 4)

	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, reasoning.HoldDecision("hold"))

	_, err := rec.Reconcile(context.Background(), reconcileRequest("production", "autoscaler"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &scalingv1alpha1.AgenticAutoscaler{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "production", Name: "autoscaler"}, updated)
	if updated.Status.CurrentReplicas != 4 {
		t.Errorf("CurrentReplicas: want 4, got %d", updated.Status.CurrentReplicas)
	}
}

// TestReconcile_EmptyNamespace_ScaleUp_PatchesDeployment exercises the *write*
// path with an empty spec.Namespace (T2.1). Unlike the hold-decision test above,
// a scale-up reaches the executor, which reads spec.Namespace directly. Before
// the fix the executor received "" and the Deployment lookup failed, so the scale
// silently errored; after the fix the controller resolves the namespace on the
// spec so the Deployment is patched.
func TestReconcile_EmptyNamespace_ScaleUp_PatchesDeployment(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "production", "api")
	aa.Spec.Namespace = "" // omitted — must resolve to the CR namespace on the write path
	deploy := makeDeployment("api", "production", 4)

	scaleUp := reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: 8,
		Confidence:     0.9,
		Reason:         "high load detected",
		Timestamp:      newTimestamp(time.Now()),
	}
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, scaleUp)

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("production", "autoscaler")); err != nil {
		t.Fatalf("Reconcile: want nil with empty spec.namespace on scale-up, got %v", err)
	}

	updatedDeploy := &appsv1.Deployment{}
	if err := rec.Get(context.Background(), types.NamespacedName{Namespace: "production", Name: "api"}, updatedDeploy); err != nil {
		t.Fatalf("Get Deployment: %v", err)
	}
	if updatedDeploy.Spec.Replicas == nil || *updatedDeploy.Spec.Replicas != 8 {
		t.Errorf("Spec.Replicas: want 8 (scaled with resolved namespace), got %v", updatedDeploy.Spec.Replicas)
	}
}

// ---- T2.2: durable sustained-quiet timer -----------------------------------

// snapshotSignalCollector returns a fixed snapshot so a test can inject log
// entries (to drive pattern matching through fusion.Correlate) and metrics.
type snapshotSignalCollector struct{ snap signals.SystemSnapshot }

func (s *snapshotSignalCollector) Collect(_ context.Context, _ scalingv1alpha1.AgenticAutoscalerSpec) (signals.SystemSnapshot, error) {
	return s.snap, nil
}

func TestReconcile_CleanReconcile_SetsLastCleanSince(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service") // Status.LastCleanSince nil
	deploy := makeDeployment("my-service", "default", 5)

	// A pattern-free snapshot → fusion matches nothing → the controller starts
	// the quiet-window timer.
	rec, _ := makeReconciler(t, s, []runtime.Object{aa, deploy}, reasoning.HoldDecision("hold"))

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &scalingv1alpha1.AgenticAutoscaler{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "autoscaler"}, updated)
	if updated.Status.LastCleanSince == nil {
		t.Fatal("Status.LastCleanSince: want non-nil after a pattern-free reconcile, got nil")
	}
}

func TestReconcile_PatternPresent_ClearsLastCleanSince(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	// Pre-seed the timer as if we had been quiet for a while.
	past := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	aa.Status.LastCleanSince = &past
	deploy := makeDeployment("my-service", "default", 5)

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{}).
		WithRuntimeObjects(aa, deploy).
		Build()

	// A snapshot whose log line matches the connection-pool-exhausted pattern.
	snap := signals.SystemSnapshot{
		LogEntries: []signals.LogEntry{{Message: "connection pool exhausted after 30s"}},
	}
	rec := &AgenticAutoscalerReconciler{
		Client:          fc,
		Scheme:          s,
		SignalCollector: &snapshotSignalCollector{snap: snap},
		Policy:          policy.NewEnforcer(&noopHPAReader{}),
		Scaler:          scaler.NewExecutor(fc),
		Observability:   observability.NewNoopRecorder(),
		NewAgent: func(_ reasoning.Config) reasoning.Agent {
			return &fixedAgent{decision: reasoning.HoldDecision("hold")}
		},
	}

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &scalingv1alpha1.AgenticAutoscaler{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "autoscaler"}, updated)
	if updated.Status.LastCleanSince != nil {
		t.Errorf("Status.LastCleanSince: want nil after a pattern reappears, got %v", updated.Status.LastCleanSince)
	}
}

// TestReconcile_DurableQuietWindow_ScaleDownFires proves the whole point of T2.2:
// with the timer carried on the CR status, the real rule-based agent (rebuilt
// fresh each reconcile) fires rule 5 once the 15-minute quiet window has elapsed.
func TestReconcile_DurableQuietWindow_ScaleDownFires(t *testing.T) {
	s := reconcilerScheme(t)
	aa := makeAA("autoscaler", "default", "my-service")
	// Quiet window already elapsed (persisted from prior reconciles).
	past := metav1.NewTime(time.Now().Add(-16 * time.Minute))
	aa.Status.LastCleanSince = &past
	deploy := makeDeployment("my-service", "default", 5)

	fc := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{}).
		WithRuntimeObjects(aa, deploy).
		Build()

	// Clean snapshot: low CPU, no log patterns → severity 0, CPU < 20%.
	snap := signals.SystemSnapshot{Metrics: signals.MetricSnapshot{CPUUtilPct: 10, LatencyP99Ms: 40}}
	rec := &AgenticAutoscalerReconciler{
		Client:          fc,
		Scheme:          s,
		SignalCollector: &snapshotSignalCollector{snap: snap},
		Policy:          policy.NewEnforcer(&noopHPAReader{}),
		Scaler:          scaler.NewExecutor(fc),
		Observability:   observability.NewNoopRecorder(),
		// The real rule-based agent — the durable CleanSince must reach it.
		NewAgent: reasoning.NewRuleBasedAgent,
	}

	if _, err := rec.Reconcile(context.Background(), reconcileRequest("default", "autoscaler")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updatedDeploy := &appsv1.Deployment{}
	_ = rec.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "my-service"}, updatedDeploy)
	if updatedDeploy.Spec.Replicas == nil || *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Spec.Replicas: want 3 (min+1, rule 5 fired via durable timer), got %v", updatedDeploy.Spec.Replicas)
	}
}
