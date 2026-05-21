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

package observability

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

// ---- helpers ---------------------------------------------------------------

func testScheme(t *testing.T) *runtime.Scheme {
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

func makeAA(deploy, ns string, dryRun bool) scalingv1alpha1.AgenticAutoscaler {
	return scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: deploy + "-autoscaler", Namespace: ns},
		Spec: scalingv1alpha1.AgenticAutoscalerSpec{
			TargetDeployment: deploy,
			Namespace:        ns,
			DryRun:           dryRun,
		},
	}
}

func makeDecision(action string, old, target int32, provider string, patterns []string) reasoning.ScaleDecision {
	ts := metav1.NewTime(time.Now())
	return reasoning.ScaleDecision{
		Action:          action,
		TargetReplicas:  target,
		Confidence:      0.9,
		Reason:          "test reason",
		Timestamp:       &ts,
		Provider:        provider,
		OldReplicas:     old,
		PatternsMatched: patterns,
	}
}

// ---- tests -----------------------------------------------------------------

// ---- tests -----------------------------------------------------------------

func TestConfigMapRecorder_CreatesConfigMapOnFirstRecord(t *testing.T) {
	s := testScheme(t)
	fc := fake.NewClientBuilder().WithScheme(s).Build()
	rec := newConfigMapRecorderForNamespace(fc, "operator-ns")

	aa := makeAA("payment-service", "production", false)
	decision := makeDecision("scale-up", 3, 8, "rule-based", []string{"connection-pool-exhausted"})

	if err := rec.Record(context.Background(), aa, decision); err != nil {
		t.Fatalf("Record: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: auditLogCMName, Namespace: "operator-ns"},
		cm); err != nil {
		t.Fatalf("ConfigMap not created: %v", err)
	}

	entries, err := unmarshalEntries(cm.Data[auditLogKey])
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries: want 1, got %d", len(entries))
	}

	e := entries[0]
	if e.Deployment != "payment-service" {
		t.Errorf("Deployment: want %q, got %q", "payment-service", e.Deployment)
	}
	if e.Action != "scale-up" {
		t.Errorf("Action: want scale-up, got %q", e.Action)
	}
	if e.OldReplicas != 3 {
		t.Errorf("OldReplicas: want 3, got %d", e.OldReplicas)
	}
	if e.NewReplicas != 8 {
		t.Errorf("NewReplicas: want 8, got %d", e.NewReplicas)
	}
	if e.Provider != "rule-based" {
		t.Errorf("Provider: want rule-based, got %q", e.Provider)
	}
	if len(e.PatternsMatched) != 1 || e.PatternsMatched[0] != "connection-pool-exhausted" {
		t.Errorf("PatternsMatched: want [connection-pool-exhausted], got %v", e.PatternsMatched)
	}
	if e.DryRun {
		t.Error("DryRun: want false")
	}
}

func TestConfigMapRecorder_AppendsToExistingConfigMap(t *testing.T) {
	s := testScheme(t)
	fc := fake.NewClientBuilder().WithScheme(s).Build()
	rec := newConfigMapRecorderForNamespace(fc, "operator-ns")

	aa := makeAA("api", "default", false)
	d1 := makeDecision("scale-up", 2, 5, "anthropic", nil)
	d2 := makeDecision("hold", 5, 5, "rule-based", nil)

	if err := rec.Record(context.Background(), aa, d1); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	if err := rec.Record(context.Background(), aa, d2); err != nil {
		t.Fatalf("second Record: %v", err)
	}

	cm := &corev1.ConfigMap{}
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: auditLogCMName, Namespace: "operator-ns"}, cm)

	entries, _ := unmarshalEntries(cm.Data[auditLogKey])
	if len(entries) != 2 {
		t.Fatalf("entries: want 2, got %d", len(entries))
	}
	if entries[0].Action != "scale-up" {
		t.Errorf("entries[0].Action: want scale-up, got %q", entries[0].Action)
	}
	if entries[1].Action != "hold" {
		t.Errorf("entries[1].Action: want hold, got %q", entries[1].Action)
	}
}

func TestConfigMapRecorder_RotatesAtMaxEntries(t *testing.T) {
	s := testScheme(t)
	fc := fake.NewClientBuilder().WithScheme(s).Build()
	rec := newConfigMapRecorderForNamespace(fc, "operator-ns")

	aa := makeAA("checkout", "staging", false)

	// Write maxEntries+10 records.
	total := maxEntries + 10
	for i := 0; i < total; i++ {
		d := makeDecision("hold", int32(i), int32(i), "rule-based", nil)
		d.OldReplicas = int32(i)
		if err := rec.Record(context.Background(), aa, d); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	cm := &corev1.ConfigMap{}
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: auditLogCMName, Namespace: "operator-ns"}, cm)

	entries, _ := unmarshalEntries(cm.Data[auditLogKey])
	if len(entries) != maxEntries {
		t.Errorf("entries after rotation: want %d, got %d", maxEntries, len(entries))
	}
	// Oldest entries must have been dropped; first remaining entry is index 10.
	if entries[0].OldReplicas != int32(total-maxEntries) {
		t.Errorf("entries[0].OldReplicas: want %d (oldest kept), got %d",
			total-maxEntries, entries[0].OldReplicas)
	}
}

func TestConfigMapRecorder_HoldDecision_NewReplicasEqualsOld(t *testing.T) {
	s := testScheme(t)
	fc := fake.NewClientBuilder().WithScheme(s).Build()
	rec := newConfigMapRecorderForNamespace(fc, "operator-ns")

	aa := makeAA("svc", "prod", false)
	// hold: TargetReplicas is 0 (unused), OldReplicas is 5
	ts := metav1.NewTime(time.Now())
	hold := reasoning.ScaleDecision{
		Action:      "hold",
		Reason:      "steady state",
		Timestamp:   &ts,
		Provider:    "rule-based",
		OldReplicas: 5,
	}

	if err := rec.Record(context.Background(), aa, hold); err != nil {
		t.Fatalf("Record: %v", err)
	}

	cm := &corev1.ConfigMap{}
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: auditLogCMName, Namespace: "operator-ns"}, cm)
	entries, _ := unmarshalEntries(cm.Data[auditLogKey])

	if entries[0].NewReplicas != 5 {
		t.Errorf("NewReplicas for hold: want 5 (== OldReplicas), got %d", entries[0].NewReplicas)
	}
	if entries[0].OldReplicas != 5 {
		t.Errorf("OldReplicas: want 5, got %d", entries[0].OldReplicas)
	}
}

func TestConfigMapRecorder_DryRun_RecordedCorrectly(t *testing.T) {
	s := testScheme(t)
	fc := fake.NewClientBuilder().WithScheme(s).Build()
	rec := newConfigMapRecorderForNamespace(fc, "operator-ns")

	aa := makeAA("api", "default", true) // dryRun=true
	decision := makeDecision("scale-up", 3, 10, "anthropic", nil)

	if err := rec.Record(context.Background(), aa, decision); err != nil {
		t.Fatalf("Record: %v", err)
	}

	cm := &corev1.ConfigMap{}
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: auditLogCMName, Namespace: "operator-ns"}, cm)
	entries, _ := unmarshalEntries(cm.Data[auditLogKey])

	if !entries[0].DryRun {
		t.Error("DryRun: want true")
	}
}

func TestConfigMapRecorder_CorruptedData_StartsFresh(t *testing.T) {
	s := testScheme(t)
	// Pre-create ConfigMap with corrupted JSON.
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: auditLogCMName, Namespace: "operator-ns"},
		Data:       map[string]string{auditLogKey: "this is not valid JSON {{{{"},
	}
	fc := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(existing).Build()
	rec := newConfigMapRecorderForNamespace(fc, "operator-ns")

	aa := makeAA("api", "default", false)
	decision := makeDecision("hold", 3, 3, "rule-based", nil)

	// Must not return an error despite corrupted existing data.
	if err := rec.Record(context.Background(), aa, decision); err != nil {
		t.Fatalf("Record with corrupted data: %v", err)
	}

	cm := &corev1.ConfigMap{}
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: auditLogCMName, Namespace: "operator-ns"}, cm)
	entries, _ := unmarshalEntries(cm.Data[auditLogKey])
	if len(entries) != 1 {
		t.Errorf("entries after corrupt recovery: want 1, got %d", len(entries))
	}
}

func TestConfigMapRecorder_NamespaceFromSpec(t *testing.T) {
	s := testScheme(t)
	fc := fake.NewClientBuilder().WithScheme(s).Build()
	rec := newConfigMapRecorderForNamespace(fc, "monitoring")

	// Spec.Namespace is empty — should fall back to aa.Namespace.
	aa := scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "autoscaler", Namespace: "production"},
		Spec: scalingv1alpha1.AgenticAutoscalerSpec{
			TargetDeployment: "api",
			Namespace:        "", // empty → falls back to aa.Namespace
		},
	}
	decision := makeDecision("hold", 3, 3, "rule-based", nil)

	if err := rec.Record(context.Background(), aa, decision); err != nil {
		t.Fatalf("Record: %v", err)
	}

	cm := &corev1.ConfigMap{}
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: auditLogCMName, Namespace: "monitoring"}, cm)
	entries, _ := unmarshalEntries(cm.Data[auditLogKey])
	if entries[0].Namespace != "production" {
		t.Errorf("Namespace: want production, got %q", entries[0].Namespace)
	}
}

func TestConfigMapRecorder_AllDecisionRecordFieldsPresent(t *testing.T) {
	s := testScheme(t)
	fc := fake.NewClientBuilder().WithScheme(s).Build()
	rec := newConfigMapRecorderForNamespace(fc, "operator-ns")

	aa := makeAA("checkout", "prod", true)
	ts := metav1.NewTime(time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	d := reasoning.ScaleDecision{
		Action:          "scale-down",
		TargetReplicas:  2,
		Confidence:      0.75,
		Reason:          "sustained low traffic",
		Timestamp:       &ts,
		Provider:        "ollama",
		OldReplicas:     8,
		PatternsMatched: []string{"low-traffic", "idle-connections"},
	}

	if err := rec.Record(context.Background(), aa, d); err != nil {
		t.Fatalf("Record: %v", err)
	}

	cm := &corev1.ConfigMap{}
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: auditLogCMName, Namespace: "operator-ns"}, cm)
	entries, _ := unmarshalEntries(cm.Data[auditLogKey])
	e := entries[0]

	checks := []struct {
		field string
		ok    bool
	}{
		{"Timestamp non-zero", !e.Timestamp.IsZero()},
		{"Deployment", e.Deployment == "checkout"},
		{"Namespace", e.Namespace == "prod"},
		{"OldReplicas", e.OldReplicas == 8},
		{"NewReplicas", e.NewReplicas == 2},
		{"Action", e.Action == "scale-down"},
		{"Reason", e.Reason == "sustained low traffic"},
		{"Confidence", e.Confidence == 0.75},
		{"Provider", e.Provider == "ollama"},
		{"PatternsMatched len", len(e.PatternsMatched) == 2},
		{"DryRun", e.DryRun},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("DecisionRecord.%s: assertion failed", c.field)
		}
	}
}

func TestNoopRecorder_ReturnsNil(t *testing.T) {
	rec := NewNoopRecorder()
	aa := makeAA("svc", "default", false)
	decision := makeDecision("hold", 3, 3, "rule-based", nil)
	if err := rec.Record(context.Background(), aa, decision); err != nil {
		t.Errorf("NoopRecorder.Record: want nil, got %v", err)
	}
}
