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
	"fmt"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/observability"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/policy"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/scaler"
)

const minRequeueInterval = 30 * time.Second

// AgenticAutoscalerReconciler reconciles a AgenticAutoscaler object.
type AgenticAutoscalerReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	SignalCollector SignalCollector
	Policy          *policy.Enforcer
	Scaler          scaler.Scaler
	Observability   observability.Recorder
	// Grafana is optional. When non-nil, a visual annotation is pushed to
	// Grafana for every real (non-hold, non-dry-run) scale action.
	Grafana *observability.GrafanaClient
	// NewAgent constructs a reasoning.Agent from the provided Config.
	// In production this is reasoning.NewAgentFromEnv; tests can substitute a fake.
	NewAgent func(cfg reasoning.Config) reasoning.Agent
}

// NewReconciler constructs an AgenticAutoscalerReconciler with production
// dependencies injected. cmd/main.go calls this — no dependency is set via a
// global variable.
func NewReconciler(c client.Client, scheme *runtime.Scheme) *AgenticAutoscalerReconciler {
	// Build an optional Grafana client — only when GRAFANA_URL is set.
	grafanaCfg := observability.GrafanaConfigFromEnv()
	var grafana *observability.GrafanaClient
	if grafanaCfg.URL != "" {
		grafana = observability.NewGrafanaClient(grafanaCfg)
	}

	return &AgenticAutoscalerReconciler{
		Client:          c,
		Scheme:          scheme,
		SignalCollector: NewDefaultSignalCollector(),
		Policy:          policy.NewEnforcer(&k8sHPAReader{client: c}),
		Scaler:          scaler.NewExecutor(c),
		Observability:   observability.NewConfigMapRecorder(c),
		Grafana:         grafana,
		NewAgent:        reasoning.NewAgentFromEnv,
	}
}

//+kubebuilder:rbac:groups=scaling.autoscaler.io,resources=agenticautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=scaling.autoscaler.io,resources=agenticautoscalers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch
//+kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile is the main reconciliation loop. It collects signals, runs the
// reasoning engine, enforces policy, executes the scale action (unless dry-run),
// and updates the CR status on every iteration.
func (r *AgenticAutoscalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrllog.FromContext(ctx)
	reconcileStart := time.Now()

	// 1. Fetch the AgenticAutoscaler CR — return nil if deleted.
	var aa scalingv1alpha1.AgenticAutoscaler
	if err := r.Get(ctx, req.NamespacedName, &aa); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Namespace for the target Deployment; defaults to the CR's own namespace.
	deployNS := aa.Spec.Namespace
	if deployNS == "" {
		deployNS = aa.Namespace
	}
	// Resolve the namespace on the spec itself so every downstream consumer sees
	// it — the policy enforcer (HPA lookup default) and the scaler executor both
	// read spec.Namespace directly. Without this, a CR that omits spec.namespace
	// would default the read path here but pass an empty namespace to Execute,
	// causing the Deployment lookup to fail silently in namespace "".
	aa.Spec.Namespace = deployNS

	// Fetch the live Deployment to capture the current replica count needed
	// by the reasoning engine and the status update.
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: aa.Spec.TargetDeployment, Namespace: deployNS}, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("target Deployment not found, requeuing",
				"deployment", aa.Spec.TargetDeployment, "namespace", deployNS)
			return ctrl.Result{RequeueAfter: minRequeueInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get deployment %s/%s: %w", deployNS, aa.Spec.TargetDeployment, err)
	}
	currentReplicas := int32(1)
	if deploy.Spec.Replicas != nil {
		currentReplicas = *deploy.Spec.Replicas
	}

	// 2. Collect signals (Prometheus + Loki). On error, back off and retry;
	// do not hold up the queue with an error return.
	snapshot, err := r.SignalCollector.Collect(ctx, aa.Spec)
	if err != nil {
		logger.Error(err, "signal collection failed, backing off")
		return ctrl.Result{RequeueAfter: minRequeueInterval}, nil
	}

	// 3. Fuse signals into a correlated FusedSignal, using the built-in patterns
	// extended/overridden by any custom spec.logPatterns. Invalid custom patterns
	// are skipped (fail-safe) and surfaced in status; fusion still runs on the
	// remaining valid set.
	patterns, patternWarnings := effectivePatterns(aa.Spec.LogPatterns)
	if len(patternWarnings) > 0 {
		logger.Info("skipped invalid custom log patterns", "warnings", patternWarnings)
	}
	fused := fusion.CorrelateWithPatterns(snapshot, patterns)

	// Advance the consecutive-window counter for THIS reconcile: a window with
	// any matched log pattern continues the streak, a clean window resets it.
	// The value must include the current window so the sustained scale-up rules
	// can fire on the reconcile that reaches the threshold.
	consecutiveWindows := aa.Status.ConsecutivePatternWindows
	if len(fused.MatchedPatterns) > 0 {
		consecutiveWindows++
	} else {
		consecutiveWindows = 0
	}
	// Effective threshold; treat an unset (zero) value as 1 for back-compat.
	windowThreshold := aa.Spec.Detection.ConsecutiveWindows
	if windowThreshold < 1 {
		windowThreshold = 1
	}

	// 4. Build a fresh Agent each reconcile — it needs the live replica count
	// which changes across reconcile cycles.
	agent := r.NewAgent(reasoning.Config{
		CurrentReplicas:  currentReplicas,
		MinReplicas:      aa.Spec.MinReplicas,
		MaxReplicas:      aa.Spec.MaxReplicas,
		TargetDeployment: aa.Spec.TargetDeployment,
		// Carry the sustained-quiet timer across reconciles so rule 5 can fire.
		// The controller rebuilds the agent every cycle (for the live replica
		// count), so an in-memory timer would reset each time; the durable
		// anchor lives on the CR status instead.
		CleanSince: aa.Status.LastCleanSince,
		// Sustained-pattern gate for scale-up rules 2 and 4. The counter is
		// carried on the CR status (like CleanSince) so it survives the agent
		// being rebuilt every reconcile.
		ConsecutivePatternWindows:  consecutiveWindows,
		ConsecutiveWindowThreshold: windowThreshold,
	})
	decision, err := agent.Decide(ctx, fused)
	if err != nil {
		// Reasoning failure is not fatal — fall back to hold and continue.
		logger.Error(err, "reasoning failed, defaulting to hold")
		observability.RecordAIProviderError(configuredProvider())
		decision = reasoning.HoldDecision("reasoning error: " + err.Error())
	}
	// Stamp fields the observability recorder needs but agents can't set themselves.
	decision.OldReplicas = currentReplicas
	decision.PatternsMatched = patternNames(fused.MatchedPatterns)

	// 5. Enforce policy: bounds, cooldown, HPA coexistence.
	prePolicyAction := decision.Action
	decision, err = r.Policy.Enforce(ctx, aa.Spec, aa.Status.LastScaleTime, decision)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("policy enforce: %w", err)
	}
	// A non-hold action converted to hold by Enforce means the cooldown was active.
	if prePolicyAction != "hold" && decision.Action == "hold" {
		observability.RecordCooldownSkip(aa.Spec.TargetDeployment)
	}
	observability.RecordDecision(decision.Action, aa.Spec.TargetDeployment, decision.Provider,
		time.Since(reconcileStart).Seconds())

	// 6. Execute the scale action — skip when dry-run or the decision is hold.
	didScale := false
	if aa.Spec.DryRun {
		logger.Info("dry-run: would apply scale decision",
			"action", decision.Action,
			"targetReplicas", decision.TargetReplicas,
			"reason", decision.Reason)
	} else if decision.Action != "hold" {
		if err := r.Scaler.Execute(ctx, aa.Spec, decision); err != nil {
			return ctrl.Result{}, fmt.Errorf("scaler execute: %w", err)
		}
		didScale = true
	}

	// 7. Update status using a merge patch so only changed fields are written.
	statusPatch := client.MergeFrom(aa.DeepCopy())
	reason := decision.Reason
	if aa.Spec.DryRun && decision.Action != "hold" {
		reason = "[dry-run] " + reason
	}
	aa.Status.LastDecisionReason = reason
	aa.Status.ObservedGeneration = aa.Generation
	aa.Status.HPACoexistenceStatus = hpaCoexistenceStatus(aa.Spec)

	// Advance the sustained-quiet timer for the next reconcile: clear it while any
	// pattern is active, and start it on the first pattern-free reconcile. This
	// durable anchor (vs. an in-memory timer) is what lets rule 5's 15-minute
	// quiet window survive the agent being rebuilt every reconcile.
	if len(fused.MatchedPatterns) > 0 {
		aa.Status.LastCleanSince = nil
	} else if aa.Status.LastCleanSince == nil {
		now := metav1.Now()
		aa.Status.LastCleanSince = &now
	}
	// Persist the consecutive-window counter computed above so the streak
	// survives into the next reconcile.
	aa.Status.ConsecutivePatternWindows = consecutiveWindows
	// Surface any custom-pattern compile failures (nil clears stale warnings).
	aa.Status.LogPatternWarnings = patternWarnings
	if didScale {
		aa.Status.LastScaleTime = decision.Timestamp
		aa.Status.CurrentReplicas = decision.TargetReplicas
	} else {
		aa.Status.CurrentReplicas = currentReplicas
	}
	if err := r.Status().Patch(ctx, &aa, statusPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("status patch: %w", err)
	}

	// 8. Observability — non-blocking. Both paths log failures but never return
	// an error to the caller; a broken audit log or unreachable Grafana must
	// never block scale actions or dry-run cycles.

	// Always record every decision: hold, dry-run, and real scale actions.
	if err := r.Observability.Record(ctx, aa, decision); err != nil {
		logger.Error(err, "observability record failed (non-fatal)")
	}

	// Push a Grafana annotation only for real, non-hold scale actions so the
	// on-call dashboard shows a vertical marker exactly when scaling happened.
	if r.Grafana != nil && decision.Action != "hold" && !aa.Spec.DryRun {
		rec := observability.NewDecisionRecord(aa, decision)
		if err := r.Grafana.PushAnnotation(ctx, rec); err != nil {
			// PushAnnotation already swallows all network/HTTP errors and returns
			// nil; this branch is unreachable in practice but kept for safety.
			logger.Error(err, "grafana annotation failed (non-fatal)")
		}
	}

	// 9. Requeue after the cooldown window; enforce a minimum interval to avoid
	// hammering the API when CooldownSeconds is zero or very small.
	requeueAfter := time.Duration(aa.Spec.CooldownSeconds) * time.Second
	if requeueAfter < minRequeueInterval {
		requeueAfter = minRequeueInterval
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// hpaCoexistenceStatus derives the HPACoexistenceStatus field value from the spec.
func hpaCoexistenceStatus(spec scalingv1alpha1.AgenticAutoscalerSpec) string {
	switch spec.HPACoexistence.Mode {
	case "owner", "calibrated":
		return spec.HPACoexistence.Mode
	default:
		return ""
	}
}

// k8sHPAReader adapts the controller-runtime client to the policy.HPAReader
// interface. pkg/policy never modifies HPAs — this adapter is read-only.
type k8sHPAReader struct {
	client client.Client
}

func (h *k8sHPAReader) GetHPA(
	ctx context.Context, namespace, name string,
) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{}
	if err := h.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, hpa); err != nil {
		return nil, err
	}
	return hpa, nil
}

// effectivePatterns merges the custom patterns from spec.logPatterns onto the
// built-in fusion.DefaultPatterns and returns the compiled set plus a warning
// for every entry that failed to compile. A custom entry that reuses a built-in
// name overrides it; an invalid entry is skipped (never fatal, per the fail-safe
// invariant) with its error collected for status.logPatternWarnings.
//
// The translation from the CRD type to fusion.Pattern lives here, not in
// pkg/fusion, so that package stays free of Kubernetes types.
func effectivePatterns(specs []scalingv1alpha1.LogPatternSpec) ([]fusion.Pattern, []string) {
	// Copy the defaults so the package-level slice is never mutated or aliased.
	patterns := make([]fusion.Pattern, len(fusion.DefaultPatterns))
	copy(patterns, fusion.DefaultPatterns)

	if len(specs) == 0 {
		return patterns, nil
	}

	indexByName := make(map[string]int, len(patterns))
	for i, p := range patterns {
		indexByName[p.Name] = i
	}

	var warnings []string
	for _, lp := range specs {
		// spec score is a 0–100 weight; fusion works in 0.0–1.0.
		p, err := fusion.CompilePattern(lp.Name, lp.Regex, lp.Severity, float64(lp.Score)/100.0)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		if idx, ok := indexByName[p.Name]; ok {
			patterns[idx] = p // custom entry overrides the built-in of the same name
		} else {
			indexByName[p.Name] = len(patterns)
			patterns = append(patterns, p)
		}
	}
	return patterns, warnings
}

// patternNames extracts the Name field from each PatternMatch so the
// observability recorder can store a flat list of pattern identifiers.
func patternNames(matches []fusion.PatternMatch) []string {
	if len(matches) == 0 {
		return nil
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name
	}
	return names
}

// configuredProvider returns the AI provider name from the environment, matching
// the value that NewAgentFromEnv resolves. Used to label ai_provider_errors_total
// when agent.Decide fails before the decision carries a Provider value.
func configuredProvider() string {
	if p := os.Getenv("AI_PROVIDER"); p != "" {
		return p
	}
	return "rule-based"
}

// reconcileTriggerPredicate filters the AgenticAutoscaler watch so the operator
// does not re-enqueue itself on its own status writes. Status is updated via the
// status subresource, which never bumps metadata.generation, so
// GenerationChangedPredicate drops those update events while still reconciling on
// create, delete, and any spec change.
//
// This stops the reconcile storm — status update → watch event → immediate
// reconcile → status update … — that drove ~20 LLM calls/min while healthy and
// once OOM-killed the kind node. Periodic signal polling is unaffected: it is
// driven by the reconciler's RequeueAfter (a direct requeue, not a watch event),
// so the operator still polls on its timer.
var reconcileTriggerPredicate = predicate.GenerationChangedPredicate{}

// SetupWithManager sets up the controller with the Manager.
func (r *AgenticAutoscalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&scalingv1alpha1.AgenticAutoscaler{}, builder.WithPredicates(reconcileTriggerPredicate)).
		Complete(r)
}
