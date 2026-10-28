# HPA coexistence rules

This rule applies whenever touching `pkg/policy/`, `api/v1alpha1/types.go`, or any code that reads/writes `HorizontalPodAutoscaler` objects.

## The problem
If both HPA and the agentic autoscaler target the same `Deployment`, they write to the same `spec.replicas` field and will fight each other. The agentic autoscaler scales up → HPA scales down a moment later → thrash.

## The coexistence modes

Mode B below keeps the operator's target inside the HPA's bounds, but both controllers still write `spec.replicas`, so a live HPA undoes the operator's scale-ups. When an HPA or KEDA ScaledObject is actively scaling the Deployment, use **Mode C (advisor)**.

### Mode C — Advisor (HPA/KEDA stays the only writer)
The operator never writes `spec.replicas`. It publishes its policy-enforced recommendation as `agentic_autoscaler_recommended_replicas{autoscaler_namespace,autoscaler,deployment_namespace,deployment}` and the user's HPA (External metric, `averageValue: "1"`) or KEDA ScaledObject (prometheus trigger, `threshold: "1"`) scales on it alongside CPU. The HPA takes the max across metrics, so the two can never fight.

```yaml
spec:
  targetDeployment: payment-service
  hpaCoexistence:
    mode: "advisor"
```

Invariants (enforced in `internal/controller`, tested in `advisor_test.go`):
1. Advisor mode never calls the scaler.
2. A hold keeps the previous recommendation; the first recommendation is `minReplicas`.
3. The series is deleted when the CR is deleted, leaves advisor mode, or is in `dryRun` — a stale value must never pin an HPA's floor.
4. A changed recommendation sets `status.lastScaleTime`, so cooldown applies exactly as in owner mode.

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
