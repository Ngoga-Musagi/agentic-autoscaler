---
name: operator-patterns
description: kubebuilder operator scaffold, Reconcile loop implementation, CRD type design, controller-runtime Manager setup, finalizers, status updates. Use when building or modifying the operator core, defining new CRD fields, or debugging reconciliation issues.
allowed-tools: Read, Grep, Glob, Bash(go:*), Bash(make:*)
---

# Operator patterns

## CRD type design (`api/v1alpha1/types.go`)

The spec is what the user declares. The status is what the operator reports back.

```go
type AgenticAutoscalerSpec struct {
    TargetDeployment string            `json:"targetDeployment"`
    Namespace        string            `json:"namespace,omitempty"`
    PrometheusURL    string            `json:"prometheusURL"`
    LogSource        LogSourceConfig   `json:"logSource"`
    MinReplicas      int32             `json:"minReplicas"`
    MaxReplicas      int32             `json:"maxReplicas"`
    CooldownSeconds  int32             `json:"cooldownSeconds"`
    DryRun           bool              `json:"dryRun,omitempty"`
    AIProvider       AIProviderConfig  `json:"aiProvider,omitempty"`
    HPACoexistence   HPACoexistence    `json:"hpaCoexistence,omitempty"`
}

type AgenticAutoscalerStatus struct {
    CurrentReplicas      int32       `json:"currentReplicas,omitempty"`
    LastScaleTime        *metav1.Time `json:"lastScaleTime,omitempty"`
    LastDecisionReason   string      `json:"lastDecisionReason,omitempty"`
    HPACoexistenceStatus string      `json:"hpaCoexistenceStatus,omitempty"`
    ObservedGeneration   int64       `json:"observedGeneration,omitempty"`
}
```

After every change to these structs: `make generate && make manifests`

## Reconcile loop skeleton (`internal/controller/reconciler.go`)

```go
func (r *AgenticAutoscalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    // 1. Fetch the CR — if not found, it was deleted, nothing to do
    var aa scalingv1alpha1.AgenticAutoscaler
    if err := r.Get(ctx, req.NamespacedName, &aa); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 2. Collect signals (pure Go, no K8s calls)
    snapshot, err := r.SignalCollector.Collect(ctx, aa.Spec)
    if err != nil {
        log.Error(err, "signal collection failed")
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }

    // 3. Fuse signals
    fused := r.Fusion.Correlate(snapshot)

    // 4. AI / rule-based decision
    decision, err := r.Agent.Decide(ctx, fused)
    if err != nil {
        log.Error(err, "reasoning failed, using hold decision")
        decision = reasoning.HoldDecision("reasoning error: " + err.Error())
    }

    // 5. Policy enforcement
    decision, err = r.Policy.Enforce(ctx, aa.Spec, decision)
    if err != nil {
        return ctrl.Result{}, err
    }

    // 6. Execute (or dry-run log)
    if !aa.Spec.DryRun && decision.Action != "hold" {
        if err := r.Scaler.Execute(ctx, aa.Spec, decision); err != nil {
            return ctrl.Result{}, err
        }
    }

    // 7. Update status
    aa.Status.LastDecisionReason = decision.Reason
    aa.Status.LastScaleTime = decision.Timestamp
    if err := r.Status().Update(ctx, &aa); err != nil {
        return ctrl.Result{}, err
    }

    // 8. Observability
    r.Observability.Record(ctx, aa, decision)

    return ctrl.Result{RequeueAfter: time.Duration(aa.Spec.CooldownSeconds) * time.Second}, nil
}
```

## Requeue strategy
- Normal reconcile: `RequeueAfter: cooldownSeconds`
- Signal error: `RequeueAfter: 30s` (back off, don't hammer)
- Fatal config error: return `err` (controller-runtime will exponential backoff)

## 8-phase implementation roadmap

| Phase | Work | Output |
|-------|------|--------|
| 1 | `kubebuilder init` + `kubebuilder create api` | Scaffold, go.mod, Makefile |
| 2 | Define Spec/Status structs + `make generate` | api/v1alpha1/, config/crd/ |
| 3 | Build pkg/signals + pkg/fusion | MetricSnapshot, FusedSignal |
| 4 | Build pkg/reasoning (rules first, AI second) | ScaleDecision + explanation |
| 5 | Implement Reconcile() + pkg/policy + pkg/scaler | Working operator on kind |
| 6 | Build pkg/observability | Grafana annotations, audit log |
| 7 | envtest integration tests + k6 load scenarios | Validated on staging |
| 8 | Helm chart + production rollout (dry-run first) | Production deployment |

## Component dependency chain
```
api/v1alpha1 (types)
    └── pkg/signals  (reads Spec.PrometheusURL, Spec.LogSource)
    └── pkg/fusion   (consumes SystemSnapshot from signals)
    └── pkg/reasoning (consumes FusedSignal from fusion)
    └── pkg/policy   (validates ScaleDecision against Spec bounds)
    └── pkg/scaler   (executes validated decision via K8s API)
    └── pkg/observability (records decision to Grafana + audit log)
internal/controller (wires all of the above in Reconcile())
```
