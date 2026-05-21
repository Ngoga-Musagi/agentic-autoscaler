package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

// ---- helpers ---------------------------------------------------------------

// fakeHPAReader is a test double for HPAReader.
type fakeHPAReader struct {
	hpa *autoscalingv2.HorizontalPodAutoscaler
	err error
}

func (f *fakeHPAReader) GetHPA(_ context.Context, _, _ string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	return f.hpa, f.err
}

// makeHPA builds a minimal HPA with the given min/max replica bounds.
func makeHPA(min, max int32) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MinReplicas: &min,
			MaxReplicas: max,
		},
	}
}

// baseSpec returns a typical AgenticAutoscalerSpec for tests that do not
// need HPA coexistence.
func baseSpec() v1alpha1.AgenticAutoscalerSpec {
	return v1alpha1.AgenticAutoscalerSpec{
		TargetDeployment: "payment-service",
		Namespace:        "production",
		MinReplicas:      2,
		MaxReplicas:      20,
		CooldownSeconds:  300,
	}
}

// scaleUp builds a scale-up ScaleDecision to the given target.
func scaleUp(target int32) reasoning.ScaleDecision {
	return reasoning.ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: target,
		Confidence:     0.9,
		Reason:         "test",
	}
}

// scaleDown builds a scale-down ScaleDecision to the given target.
func scaleDown(target int32) reasoning.ScaleDecision {
	return reasoning.ScaleDecision{
		Action:         "scale-down",
		TargetReplicas: target,
		Confidence:     0.7,
		Reason:         "test",
	}
}

// holdDecision returns a hold ScaleDecision (no target replicas).
func holdDecision() reasoning.ScaleDecision {
	return reasoning.ScaleDecision{Action: "hold", Reason: "test"}
}

// newEnforcerWithClock returns an Enforcer whose clock is fixed at now.
// hpaReader may be nil for tests that do not exercise calibrated mode.
func newEnforcerWithClock(hpaReader HPAReader, now time.Time) *Enforcer {
	e := NewEnforcer(hpaReader)
	e.now = func() time.Time { return now }
	return e
}

// recentScaleTime returns a *metav1.Time representing a scale that happened
// delta ago relative to the fixed now.
func recentScaleTime(now time.Time, delta time.Duration) *metav1.Time {
	t := metav1.NewTime(now.Add(-delta))
	return &t
}

// ---- bounds enforcement ----------------------------------------------------

func TestEnforcer_DecisionWithinBounds_PassesThrough(t *testing.T) {
	e := newEnforcerWithClock(nil, time.Now())
	spec := baseSpec() // min=2, max=20

	got, err := e.Enforce(context.Background(), spec, nil, scaleUp(8))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Action != "scale-up" {
		t.Errorf("Action: want %q, got %q", "scale-up", got.Action)
	}
	if got.TargetReplicas != 8 {
		t.Errorf("TargetReplicas: want 8, got %d", got.TargetReplicas)
	}
}

func TestEnforcer_DecisionAboveMaxReplicas_ClampsToMax(t *testing.T) {
	e := newEnforcerWithClock(nil, time.Now())
	spec := baseSpec() // max=20

	got, err := e.Enforce(context.Background(), spec, nil, scaleUp(99))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.TargetReplicas != 20 {
		t.Errorf("TargetReplicas: want 20 (clamped to max), got %d", got.TargetReplicas)
	}
	if got.Action != "scale-up" {
		t.Errorf("Action: want %q (preserved), got %q", "scale-up", got.Action)
	}
}

func TestEnforcer_DecisionBelowMinReplicas_ClampsToMin(t *testing.T) {
	e := newEnforcerWithClock(nil, time.Now())
	spec := baseSpec() // min=2

	got, err := e.Enforce(context.Background(), spec, nil, scaleDown(1))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.TargetReplicas != 2 {
		t.Errorf("TargetReplicas: want 2 (clamped to min), got %d", got.TargetReplicas)
	}
}

// ---- hold pass-through -----------------------------------------------------

func TestEnforcer_HoldDecision_PassesThroughUnchanged(t *testing.T) {
	// A hold decision must not be affected by cooldown or bounds checks.
	now := time.Now()
	// Cooldown is active — 30 seconds elapsed out of a 300-second window.
	lastScale := recentScaleTime(now, 30*time.Second)
	e := newEnforcerWithClock(nil, now)

	got, err := e.Enforce(context.Background(), baseSpec(), lastScale, holdDecision())
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q, got %q", "hold", got.Action)
	}
}

// ---- cooldown --------------------------------------------------------------

func TestEnforcer_WithinCooldownWindow_ReturnsHold(t *testing.T) {
	now := time.Now()
	// Last scale was 60 s ago; cooldown window is 300 s → still active.
	lastScale := recentScaleTime(now, 60*time.Second)
	e := newEnforcerWithClock(nil, now)

	got, err := e.Enforce(context.Background(), baseSpec(), lastScale, scaleUp(10))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q (cooldown hold), got %q", "hold", got.Action)
	}
}

func TestEnforcer_CooldownExpired_DecisionPassesThrough(t *testing.T) {
	now := time.Now()
	// Last scale was 600 s ago; cooldown window is 300 s → expired.
	lastScale := recentScaleTime(now, 600*time.Second)
	e := newEnforcerWithClock(nil, now)

	got, err := e.Enforce(context.Background(), baseSpec(), lastScale, scaleUp(10))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Action != "scale-up" {
		t.Errorf("Action: want %q, got %q", "scale-up", got.Action)
	}
}

func TestEnforcer_NilLastScaleTime_NoCooldown(t *testing.T) {
	e := newEnforcerWithClock(nil, time.Now())

	got, err := e.Enforce(context.Background(), baseSpec(), nil, scaleUp(8))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Action != "scale-up" {
		t.Errorf("Action: want %q (no cooldown on first scale), got %q", "scale-up", got.Action)
	}
}

func TestEnforcer_ZeroCooldownSeconds_NeverBlocks(t *testing.T) {
	now := time.Now()
	spec := baseSpec()
	spec.CooldownSeconds = 0
	// Scale happened 1 second ago — would block with any non-zero cooldown.
	lastScale := recentScaleTime(now, 1*time.Second)
	e := newEnforcerWithClock(nil, now)

	got, err := e.Enforce(context.Background(), spec, lastScale, scaleUp(8))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Action != "scale-up" {
		t.Errorf("Action: want %q (zero cooldown should never block), got %q", "scale-up", got.Action)
	}
}

// ---- calibrated HPA coexistence --------------------------------------------

func TestEnforcer_CalibratedMode_ClampsToHPAMax(t *testing.T) {
	// HPA allows up to 12 replicas; decision asks for 15 → clamp to 12.
	reader := &fakeHPAReader{hpa: makeHPA(2, 12)}
	e := newEnforcerWithClock(reader, time.Now())

	spec := baseSpec()
	spec.MaxReplicas = 20 // CR allows 20, but HPA only allows 12
	spec.HPACoexistence = v1alpha1.HPACoexistence{
		Mode:         "calibrated",
		HPAName:      "payment-service-hpa",
		HPANamespace: "production",
	}

	got, err := e.Enforce(context.Background(), spec, nil, scaleUp(15))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.TargetReplicas != 12 {
		t.Errorf("TargetReplicas: want 12 (clamped to HPA max), got %d", got.TargetReplicas)
	}
}

func TestEnforcer_CalibratedMode_ClampsToHPAMin(t *testing.T) {
	// HPA requires at least 4 replicas; decision asks for 2 → clamp to 4.
	reader := &fakeHPAReader{hpa: makeHPA(4, 20)}
	e := newEnforcerWithClock(reader, time.Now())

	spec := baseSpec()
	spec.MinReplicas = 2
	spec.HPACoexistence = v1alpha1.HPACoexistence{
		Mode:         "calibrated",
		HPAName:      "payment-service-hpa",
		HPANamespace: "production",
	}

	got, err := e.Enforce(context.Background(), spec, nil, scaleDown(2))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.TargetReplicas != 4 {
		t.Errorf("TargetReplicas: want 4 (clamped to HPA min), got %d", got.TargetReplicas)
	}
}

func TestEnforcer_CalibratedMode_WithinHPABounds_Unchanged(t *testing.T) {
	reader := &fakeHPAReader{hpa: makeHPA(2, 20)}
	e := newEnforcerWithClock(reader, time.Now())

	spec := baseSpec()
	spec.HPACoexistence = v1alpha1.HPACoexistence{
		Mode:         "calibrated",
		HPAName:      "payment-service-hpa",
		HPANamespace: "production",
	}

	got, err := e.Enforce(context.Background(), spec, nil, scaleUp(8))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.TargetReplicas != 8 {
		t.Errorf("TargetReplicas: want 8 (within HPA bounds), got %d", got.TargetReplicas)
	}
}

func TestEnforcer_CalibratedMode_HPAFetchError_ReturnsError(t *testing.T) {
	reader := &fakeHPAReader{err: errors.New("hpa not found")}
	e := newEnforcerWithClock(reader, time.Now())

	spec := baseSpec()
	spec.HPACoexistence = v1alpha1.HPACoexistence{
		Mode:         "calibrated",
		HPAName:      "payment-service-hpa",
		HPANamespace: "production",
	}

	_, err := e.Enforce(context.Background(), spec, nil, scaleUp(8))
	if err == nil {
		t.Fatal("Enforce: want error when HPA fetch fails, got nil")
	}
}

func TestEnforcer_CalibratedMode_NilHPAReader_ReturnsError(t *testing.T) {
	e := newEnforcerWithClock(nil, time.Now())

	spec := baseSpec()
	spec.HPACoexistence = v1alpha1.HPACoexistence{
		Mode:    "calibrated",
		HPAName: "payment-service-hpa",
	}

	_, err := e.Enforce(context.Background(), spec, nil, scaleUp(8))
	if err == nil {
		t.Fatal("Enforce: want error when HPAReader is nil in calibrated mode, got nil")
	}
}

func TestEnforcer_OwnerMode_DoesNotFetchHPA(t *testing.T) {
	// In "owner" mode the HPA is disabled — the enforcer must not call GetHPA
	// (the fakeHPAReader would return an error if called).
	reader := &fakeHPAReader{err: errors.New("should not be called")}
	e := newEnforcerWithClock(reader, time.Now())

	spec := baseSpec()
	spec.HPACoexistence = v1alpha1.HPACoexistence{Mode: "owner"}

	got, err := e.Enforce(context.Background(), spec, nil, scaleUp(8))
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.TargetReplicas != 8 {
		t.Errorf("TargetReplicas: want 8, got %d", got.TargetReplicas)
	}
}

// ---- CooldownChecker unit tests --------------------------------------------

func TestCooldownChecker_NilLastScaleTime_Inactive(t *testing.T) {
	c := &CooldownChecker{}
	if c.IsActive(nil, 300, time.Now()) {
		t.Error("IsActive: want false for nil lastScaleTime")
	}
}

func TestCooldownChecker_ZeroLastScaleTime_Inactive(t *testing.T) {
	c := &CooldownChecker{}
	zero := metav1.NewTime(time.Time{})
	if c.IsActive(&zero, 300, time.Now()) {
		t.Error("IsActive: want false for zero lastScaleTime")
	}
}

func TestCooldownChecker_WithinWindow_Active(t *testing.T) {
	c := &CooldownChecker{}
	now := time.Now()
	last := metav1.NewTime(now.Add(-60 * time.Second)) // 60s ago
	if !c.IsActive(&last, 300, now) {                  // 300s window
		t.Error("IsActive: want true when 60s elapsed of 300s window")
	}
}

func TestCooldownChecker_WindowExpired_Inactive(t *testing.T) {
	c := &CooldownChecker{}
	now := time.Now()
	last := metav1.NewTime(now.Add(-600 * time.Second)) // 600s ago
	if c.IsActive(&last, 300, now) {                    // 300s window expired
		t.Error("IsActive: want false when 600s elapsed of 300s window")
	}
}

func TestCooldownChecker_ExactlyAtBoundary_Inactive(t *testing.T) {
	// Elapsed == cooldown → window just expired; must not block.
	c := &CooldownChecker{}
	now := time.Now()
	last := metav1.NewTime(now.Add(-300 * time.Second))
	if c.IsActive(&last, 300, now) {
		t.Error("IsActive: want false when elapsed == cooldownSeconds (boundary is exclusive)")
	}
}
