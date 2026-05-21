# HPA coexistence rules

This rule applies whenever touching `pkg/policy/`, `api/v1alpha1/types.go`, or any code that reads/writes `HorizontalPodAutoscaler` objects.

## The problem
If both HPA and the agentic autoscaler target the same `Deployment`, they write to the same `spec.replicas` field and will fight each other. The agentic autoscaler scales up → HPA scales down a moment later → thrash.

## The two safe coexistence modes

### Mode A — Agentic owns the deployment (HPA disabled)
The agentic autoscaler takes full control. HPA is deleted or suspended for this deployment.

```yaml
# AgenticAutoscaler spec
spec:
  targetDeployment: payment-service
  hpaCoexistence:
    mode: "owner"          # agentic autoscaler is sole controller
  minReplicas: 2
  maxReplicas: 20
```

**When to use**: high-confidence deployments after the operator has been running for 2+ weeks.

### Mode B — Calibrated coexistence (HPA still active, bounds nested)
The agentic autoscaler's `maxReplicas` is always ≤ HPA's `maxReplicas`.
The agentic autoscaler's `minReplicas` is always ≥ HPA's `minReplicas`.
This means the agentic autoscaler operates inside the HPA's envelope — HPA acts as a safety net.

```yaml
# AgenticAutoscaler spec
spec:
  targetDeployment: payment-service
  hpaCoexistence:
    mode: "calibrated"        # nested within HPA bounds
    hpaName: "payment-service-hpa"
    hpaNamespace: "production"
  minReplicas: 3              # must be >= HPA minReplicas
  maxReplicas: 15             # must be <= HPA maxReplicas
```

**When to use**: initial rollout — HPA remains a fallback if the agentic autoscaler malfunctions.

## Implementation rules for `pkg/policy/`

1. `enforcer.go` must read the HPA bounds when `mode: calibrated` before validating a `ScaleDecision`
2. If the agentic autoscaler's proposed replica count would exceed HPA's `maxReplicas`, clamp to HPA max and log a warning
3. The `pkg/policy` package must never modify or delete HPA objects — read-only access only
4. Status field `status.hpaCoexistenceStatus` must reflect the current mode: `"owner"`, `"calibrated"`, or `"conflict-detected"`

## Opt-in annotation on Deployment (how users choose)

Users opt a deployment into agentic autoscaling by:
1. Creating an `AgenticAutoscaler` CR targeting it, AND
2. Optionally adding the annotation `autoscaler.io/agentic: "enabled"` on the Deployment

The operator ignores any Deployment without a corresponding `AgenticAutoscaler` CR — no annotation alone is sufficient.
