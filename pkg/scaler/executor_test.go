package scaler

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

// ---- shared helpers --------------------------------------------------------

// toInt64 converts a JSON numeric value stored in an unstructured map to int64.
// The fake client does not do a JSON roundtrip, so values remain int64 (not float64).
// A real API server would return float64 after JSON decode; this handles both.
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int32:
		return int64(n)
	default:
		return 0
	}
}

// newTestScheme returns a scheme with appsv1 registered for Deployment tests.
func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	return s
}

// makeTestDeployment creates a minimal Deployment with a fixed replica count.
func makeTestDeployment(name, ns string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
}

// testSpec returns an AgenticAutoscalerSpec targeting the given Deployment.
func testSpec(name, ns string) v1alpha1.AgenticAutoscalerSpec {
	return v1alpha1.AgenticAutoscalerSpec{
		TargetDeployment: name,
		Namespace:        ns,
		MinReplicas:      2,
		MaxReplicas:      20,
	}
}

// scaleUpDecision returns a scale-up decision to the given target.
func scaleUpDecision(target int32) reasoning.ScaleDecision {
	return reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: target,
		Confidence:     0.9,
		Reason:         "connection pool exhausted with high latency",
	}
}

// ---- Executor tests --------------------------------------------------------

func TestExecutor_Execute_PatchesDeploymentReplicas(t *testing.T) {
	deploy := makeTestDeployment("payment-service", "production", 3)
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(deploy).
		Build()

	e := NewExecutor(fc)
	err := e.Execute(context.Background(), testSpec("payment-service", "production"), scaleUpDecision(8))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Verify the Deployment was patched.
	updated := &appsv1.Deployment{}
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "payment-service", Namespace: "production"},
		updated); err != nil {
		t.Fatalf("Get after patch: %v", err)
	}
	if updated.Spec.Replicas == nil {
		t.Fatal("Spec.Replicas: want non-nil after patch")
	}
	if *updated.Spec.Replicas != 8 {
		t.Errorf("Spec.Replicas: want 8, got %d", *updated.Spec.Replicas)
	}
}

func TestExecutor_Execute_ScaleDown_PatchesReplicas(t *testing.T) {
	deploy := makeTestDeployment("checkout", "staging", 10)
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(deploy).
		Build()

	decision := reasoning.ScaleDecision{
		Action:         "scale-down",
		TargetReplicas: 3,
		Confidence:     0.7,
		Reason:         "low load",
	}
	if err := NewExecutor(fc).Execute(context.Background(), testSpec("checkout", "staging"), decision); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	updated := &appsv1.Deployment{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: "checkout", Namespace: "staging"}, updated)
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 3 {
		t.Errorf("Spec.Replicas: want 3, got %v", updated.Spec.Replicas)
	}
}

func TestExecutor_Execute_Hold_IsNoOp(t *testing.T) {
	deploy := makeTestDeployment("api", "default", 5)
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(deploy).
		Build()

	holdDecision := reasoning.ScaleDecision{Action: "hold", Reason: "steady state"}
	if err := NewExecutor(fc).Execute(context.Background(), testSpec("api", "default"), holdDecision); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Deployment replicas must be unchanged.
	updated := &appsv1.Deployment{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: "api", Namespace: "default"}, updated)
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 5 {
		t.Errorf("Spec.Replicas: want 5 (unchanged for hold), got %v", updated.Spec.Replicas)
	}
}

func TestExecutor_Execute_DeploymentNotFound_ReturnsError(t *testing.T) {
	// Empty fake client — no Deployment registered.
	fc := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()

	err := NewExecutor(fc).Execute(
		context.Background(),
		testSpec("missing-service", "production"),
		scaleUpDecision(8),
	)
	if err == nil {
		t.Fatal("Execute: want error for missing Deployment, got nil")
	}
}

func TestExecutor_Execute_EmptyNamespace_ReturnsClearError(t *testing.T) {
	// A Deployment exists, but the spec carries an empty namespace. Execute must
	// fail loudly rather than silently looking up namespace "" (T2.1 defense in depth).
	deploy := makeTestDeployment("payment-service", "production", 3)
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(deploy).
		Build()

	spec := testSpec("payment-service", "") // namespace intentionally empty
	err := NewExecutor(fc).Execute(context.Background(), spec, scaleUpDecision(8))
	if err == nil {
		t.Fatal("Execute: want error for empty namespace, got nil")
	}

	// The Deployment must be untouched — no scale happened.
	updated := &appsv1.Deployment{}
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: "payment-service", Namespace: "production"}, updated)
	if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 3 {
		t.Errorf("Spec.Replicas: want 3 (unchanged), got %v", updated.Spec.Replicas)
	}
}

// ---- KEDAExecutor tests ----------------------------------------------------

func TestKEDAExecutor_Execute_CreatesScaledObject(t *testing.T) {
	// Fake client with no existing ScaledObject.
	fc := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()

	ke := NewKEDAExecutor(fc)
	spec := testSpec("payment-service", "production")
	if err := ke.Execute(context.Background(), spec, scaleUpDecision(8)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// ScaledObject must now exist.
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(scaledObjectGVK)
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "payment-service-agentic", Namespace: "production"},
		got); err != nil {
		t.Fatalf("Get ScaledObject after create: %v", err)
	}

	gotSpec, ok := got.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatalf("ScaledObject spec: want map[string]interface{}, got %T", got.Object["spec"])
	}
	ref, ok := gotSpec["scaleTargetRef"].(map[string]interface{})
	if !ok || ref["name"] != "payment-service" {
		t.Errorf("scaleTargetRef.name: want %q, got %v", "payment-service", ref)
	}
	if toInt64(gotSpec["minReplicaCount"]) != int64(spec.MinReplicas) {
		t.Errorf("minReplicaCount: want %d, got %v", spec.MinReplicas, gotSpec["minReplicaCount"])
	}
	if toInt64(gotSpec["maxReplicaCount"]) != int64(spec.MaxReplicas) {
		t.Errorf("maxReplicaCount: want %d, got %v", spec.MaxReplicas, gotSpec["maxReplicaCount"])
	}
}

func TestKEDAExecutor_Execute_PatchesExistingScaledObject(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	ke := NewKEDAExecutor(fc)

	// First Execute → creates the ScaledObject with min=2, max=20.
	spec := testSpec("api", "production")
	if err := ke.Execute(context.Background(), spec, scaleUpDecision(5)); err != nil {
		t.Fatalf("first Execute (create): %v", err)
	}

	// Change spec bounds and Execute again → should patch the existing ScaledObject.
	spec.MinReplicas = 4
	spec.MaxReplicas = 15
	if err := ke.Execute(context.Background(), spec, scaleUpDecision(8)); err != nil {
		t.Fatalf("second Execute (patch): %v", err)
	}

	// Verify updated bounds.
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(scaledObjectGVK)
	_ = fc.Get(context.Background(),
		types.NamespacedName{Name: "api-agentic", Namespace: "production"},
		got)

	gotSpec := got.Object["spec"].(map[string]interface{})
	if toInt64(gotSpec["minReplicaCount"]) != 4 {
		t.Errorf("minReplicaCount after patch: want 4, got %v", gotSpec["minReplicaCount"])
	}
	if toInt64(gotSpec["maxReplicaCount"]) != 15 {
		t.Errorf("maxReplicaCount after patch: want 15, got %v", gotSpec["maxReplicaCount"])
	}
}

func TestKEDAExecutor_Execute_Hold_IsNoOp(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()

	holdDecision := reasoning.ScaleDecision{Action: "hold", Reason: "steady state"}
	if err := NewKEDAExecutor(fc).Execute(context.Background(), testSpec("api", "default"), holdDecision); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// No ScaledObject should have been created.
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(scaledObjectGVK)
	err := fc.Get(context.Background(),
		types.NamespacedName{Name: "api-agentic", Namespace: "default"},
		got)
	if err == nil {
		t.Error("hold decision should not create a ScaledObject")
	}
}

func TestKEDAExecutor_Execute_ScaledObjectName_UsesTargetDeploymentSuffix(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	spec := testSpec("checkout-service", "production")

	if err := NewKEDAExecutor(fc).Execute(context.Background(), spec, scaleUpDecision(5)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Name must be "<targetDeployment>-agentic".
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(scaledObjectGVK)
	if err := fc.Get(context.Background(),
		types.NamespacedName{Name: "checkout-service-agentic", Namespace: "production"},
		got); err != nil {
		t.Errorf("ScaledObject not found with expected name: %v", err)
	}
}
