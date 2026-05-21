---
name: hpa-coexistence
description: HPA and agentic autoscaler coexistence patterns. Use when configuring hpaCoexistence in an AgenticAutoscaler spec, implementing cooperative scaling logic, calibrating bounds relative to HPA, or preventing replica conflicts. Covers the two coexistence modes and the bounds relationship rule.
allowed-tools: Read, Grep, Glob
---

# HPA coexistence

## The core rule

When `hpaCoexistence.enabled: true`, the agentic autoscaler's `minReplicas` and `maxReplicas` MUST be calibrated INSIDE the HPA's min/max range. Both controllers write to the same `spec.replicas` field — the one that runs last wins.

```
HPA range:              [2 ──────────────────────────── 20]
Agentic autoscaler:          [4 ──────────────── 16]
                              ↑                  ↑
                     agenticMin ≥ hpaMin    agenticMax ≤ hpaMax
```

If the agentic autoscaler scales to a value the HPA immediately undoes, you have a conflict. The bounds relationship prevents this.

## Two coexistence modes

### Mode 1: Cooperative (recommended)
Agentic autoscaler reads current HPA desired replicas before deciding. It only overrides HPA if its own confidence is higher.

```go
// pkg/policy/enforcer.go

func (e *Enforcer) ShouldOverrideHPA(
    decision ScaleDecision,
    hpaDesired int32,
    hpaCurrentReplicas int32,
) bool {
    // Only act if our confidence is materially higher than HPA's threshold-based view
    if decision.Confidence < 0.75 { return false }
    // Only act if we're suggesting more replicas than HPA currently has
    if decision.Direction == ScaleUp && decision.TargetReplicas > hpaCurrentReplicas { return true }
    // Scale down only if HPA has not recently scaled up (last 2 minutes)
    return false
}
```

### Mode 2: Independent (no HPA on this deployment)
Disable HPA entirely on services managed by the agentic autoscaler. Cleanest option — no conflict possible.

```bash
# Disable HPA for a specific deployment
kubectl delete hpa payment-service -n payments

# Or: set HPA minReplicas == maxReplicas to freeze it, then manage replicas entirely via agentic autoscaler
```

## Reading HPA state in the reconciler

```go
// internal/controller/reconciler.go

func (r *Reconciler) getHPAState(ctx context.Context, ns, name string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
    hpa := &autoscalingv2.HorizontalPodAutoscaler{}
    err := r.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, hpa)
    if apierrors.IsNotFound(err) { return nil, nil }
    return hpa, err
}
```

## AgenticAutoscaler spec — calibrated bounds example

```yaml
# payment-service is managed by HPA with min=2, max=20
spec:
  minReplicas: 4          # higher than HPA min (2) — we never scale below what we trust
  maxReplicas: 16         # lower than HPA max (20) — HPA can still burst beyond us
  hpaCoexistence:
    enabled: true
    hpaName: payment-service-hpa
    mode: cooperative     # or: independent
    overrideMinConfidence: 0.75
```

## Validating bounds at admission time

The CRD webhook (optional but recommended) should validate:

```go
func validateCoexistenceBounds(spec AgenticAutoscalerSpec, hpa *autoscalingv2.HPA) error {
    if spec.HPACoexistence.Enabled {
        hpaMin := *hpa.Spec.MinReplicas
        hpaMax := hpa.Spec.MaxReplicas
        if spec.MinReplicas < hpaMin {
            return fmt.Errorf("minReplicas %d must be >= HPA minReplicas %d", spec.MinReplicas, hpaMin)
        }
        if spec.MaxReplicas > hpaMax {
            return fmt.Errorf("maxReplicas %d must be <= HPA maxReplicas %d", spec.MaxReplicas, hpaMax)
        }
    }
    return nil
}
```

## Cooldown interaction

HPA has its own cooldown (default 5 minutes for scale-down). The agentic autoscaler cooldown is separate and should be set shorter for scale-up, longer for scale-down:

```yaml
spec:
  cooldownSeconds: 120        # scale-up cooldown — 2 minutes
  scaleDownCooldownSeconds: 600  # scale-down cooldown — 10 minutes (longer than HPA default)
```

## Anti-patterns

- Never set `minReplicas` lower than the HPA's `minReplicas` — HPA will immediately undo it
- Never set `maxReplicas` higher than the HPA's `maxReplicas` — the two will fight
- Never disable the cooldown when running alongside HPA — two controllers firing simultaneously causes replica thrashing
- Never delete an HPA that you didn't create — check with the team first
