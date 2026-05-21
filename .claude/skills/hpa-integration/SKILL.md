---
name: hpa-integration
description: HPA coexistence modes, opt-in per deployment via AgenticAutoscaler CR, cooldown enforcement, min/max bounds calibration, conflict avoidance. Use when working on pkg/policy, the HPA coexistence field in the CRD spec, or any code that reads HorizontalPodAutoscaler objects.
allowed-tools: Read, Grep, Glob
---

# HPA integration

## Opt-in model — how a deployment gets agentic autoscaling

A deployment receives agentic autoscaling **only** when:
1. An `AgenticAutoscaler` CR exists that names it in `spec.targetDeployment`
2. The operator is running in the same cluster

No annotation on the Deployment is required. The CR alone is the opt-in signal.
The operator must never touch any Deployment that has no corresponding CR.

## Example CR with HPA coexistence

```yaml
apiVersion: scaling.autoscaler.io/v1alpha1
kind: AgenticAutoscaler
metadata:
  name: payment-service-autoscaler
  namespace: production
spec:
  targetDeployment: payment-service     # opt-in: only THIS deployment is managed
  prometheusURL: "http://prometheus.monitoring:9090"
  logSource:
    type: loki
    loki:
      url: "http://loki.monitoring:3100"
      query: '{app="payment-service"}'
      lookbackMinutes: 5
  minReplicas: 3
  maxReplicas: 15
  cooldownSeconds: 180
  dryRun: true                          # always start dry-run
  aiProvider:
    provider: anthropic                 # or "openai" or "ollama"
    secretRef: "ai-api-key"
  hpaCoexistence:
    mode: calibrated                    # "owner" or "calibrated"
    hpaName: payment-service-hpa
    hpaNamespace: production
```

## HPA coexistence modes

### `mode: owner`
The `AgenticAutoscaler` is the sole replica controller.
The HPA for this deployment should be deleted or suspended before enabling.
Use after 2+ weeks of validated agentic autoscaling in calibrated mode.

### `mode: calibrated` (recommended for rollout)
Both HPA and agentic autoscaler are active. The agentic autoscaler operates
inside the HPA's envelope:
- `spec.minReplicas` ≥ HPA's `minReplicas`
- `spec.maxReplicas` ≤ HPA's `maxReplicas`

The policy enforcer reads the live HPA object before approving any `ScaleDecision`
and clamps the target replica count to stay inside the HPA bounds.

## Policy enforcer (`pkg/policy/enforcer.go`)

```go
func (e *Enforcer) Enforce(ctx context.Context, spec Spec, decision ScaleDecision) (ScaleDecision, error) {
    // 1. Clamp to spec bounds
    decision.TargetReplicas = clamp(decision.TargetReplicas, spec.MinReplicas, spec.MaxReplicas)

    // 2. Cooldown check
    if spec.Status.LastScaleTime != nil {
        elapsed := time.Since(spec.Status.LastScaleTime.Time)
        if elapsed < time.Duration(spec.CooldownSeconds)*time.Second {
            return hold("cooldown active, " + elapsed.String() + " elapsed"), nil
        }
    }

    // 3. Calibrated HPA bounds check
    if spec.HPACoexistence.Mode == "calibrated" {
        hpa, err := e.getHPA(ctx, spec.HPACoexistence.HPAName, spec.HPACoexistence.HPANamespace)
        if err == nil {
            decision.TargetReplicas = clamp(decision.TargetReplicas, *hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
        }
    }

    return decision, nil
}
```

The enforcer **never** modifies or deletes HPA objects. Read-only access only.

## Deployment manifest example (where the CR lives relative to the app)

In a typical GitOps repo, the `AgenticAutoscaler` CR lives alongside the `Deployment`:

```
services/payment-service/
├── deployment.yaml          # existing Deployment
├── service.yaml
├── hpa.yaml                 # existing HPA (still active in calibrated mode)
└── agentic-autoscaler.yaml  # NEW — opt-in CR
```

The CR is a separate file. The Deployment itself is unmodified.
HEREDOC
