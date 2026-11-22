# Governance: who decides, and how to overrule it

The reasoning step (rules or an LLM) only **recommends**. A separate policy
layer is the sole authority over the replica count, and every guardrail below
can only *narrow* what the operator may do — none can widen `minReplicas` /
`maxReplicas` or skip the cooldown. All of them are off by default; an
`AgenticAutoscaler` without `spec.policy` behaves exactly as it always has.

## The order every decision goes through

```
signals → fusion → reasoning (rules | LLM)
        → confidence floor        spec.policy.minConfidence         (AI only)
        → workload guards         spec.agentic                      (429 / token budget)
        → pause                   agentic-autoscaler.io/pause, PAUSE_ALL
        → policy.Enforce          cooldown → min/max → HPA bounds → maxScaleStep → at-bound hold
        → approval gate           spec.policy.approvalAboveReplicas
        → act                     patch replicas (owner) | publish metric (advisor) | nothing (dryRun)
```

Whichever guardrail changes a decision is recorded: `status.lastGuardrail`,
the `guardrail` field of the audit record and the decision log line, and the
`agentic_autoscaler_guardrail_total{deployment,guardrail}` metric. A held
decision keeps what it would have done in its reason, e.g.

> `cooldown active (60s window); would have scaled to 8 replicas: connection pool exhausted with p99 latency spike (replicas 5 → 8)`

## Guardrails

| Control | Set with | Effect |
|---|---|---|
| Bounds | `minReplicas`, `maxReplicas` | every decision clamped; a clamp to the current count becomes an explained hold |
| Cooldown | `cooldownSeconds` | no scale action inside the window |
| Step cap | `spec.policy.maxScaleStep: 3` | one decision moves at most ±3 replicas |
| Confidence floor | `spec.policy.minConfidence: 70` | an LLM decision below 0.70 is discarded; the rule engine decides |
| Approval gate | `spec.policy.approvalAboveReplicas: 15` | scale-ups past 15 wait in `status.pendingApproval` |
| Rate-limit hold | `spec.agentic.llmRateLimitQuery` (+ `rateLimitHoldPercent`, default 20) | scale-ups held while the LLM provider rejects ≥ 20 % of calls |
| Cost guardrail | `spec.agentic.tokenBudgetQuery` (+ `tokenBudgetCapPercent`, default 90) | scale-ups held at ≥ 90 % of the token budget |
| Pause | annotation `agentic-autoscaler.io/pause: "true"` on the CR **or** the Deployment | every decision holds |
| Kill switch | Helm `pauseAll: true` (env `AGENTIC_AUTOSCALER_PAUSE_ALL`) | every CR in the cluster holds |
| Shadow | `dryRun: true` | decide and explain, never act (and never publish an advisor metric) |

The workload guards apply to **every** provider's decision, not just the rule
engine's, so an LLM cannot reason its way past them. OOM emergencies are the
one exception: they are never held by the workload guards.

### Approving a large scale-up

```bash
kubectl -n shop get agenticautoscaler checkout-autoscaler -o jsonpath='{.status.pendingApproval}'
# {"reason":"…","since":"…","targetReplicas":18}
kubectl -n shop annotate agenticautoscaler checkout-autoscaler \
  agentic-autoscaler.io/approved-replicas=18 --overwrite
```

The approval is a ceiling: it allows any scale-up up to 18 until you change or
remove it. Pending approvals are not Kubernetes Events: emitting Events would
need `events: create` on the operator ServiceAccount, which the security rules
deliberately leave out. Alert on `status.pendingApproval` (or on
`agentic_autoscaler_guardrail_total{guardrail="approval-required"}`) instead.

### Pausing

```bash
kubectl -n shop annotate deploy checkout agentic-autoscaler.io/pause=true        # one service
helm upgrade agentic-autoscaler deploy/helm --reuse-values --set pauseAll=true   # everything
```

While paused the operator keeps deciding and explaining — the audit log shows
what it *would* have done — but changes nothing. In advisor mode the published
recommendation freezes at its last value; to withdraw it entirely set
`dryRun: true` or delete the CR (the series disappears and your HPA falls back
to its own metrics).

## Multi-team clusters

The operator's own ServiceAccount stays minimal (see
`.claude/rules/security.md`): read/patch Deployments, read-only HPAs, manage
its CRs, no Secrets. Teams get access to **their** autoscalers through the
chart's tenant roles, bound per namespace:

```bash
kubectl -n shop create rolebinding shop-autoscaling \
  --clusterrole=agentic-autoscaler-editor --group=team-shop
kubectl -n shop create rolebinding shop-autoscaling-view \
  --clusterrole=agentic-autoscaler-viewer --group=sre-oncall
```

`agentic-autoscaler-editor` covers creating, editing, pausing and approving
`AgenticAutoscaler`s in that namespace — nothing else. Set
`tenantRoles.aggregateToDefaultRoles: true` to fold them into the built-in
`admin`/`edit`/`view` roles instead.

Keep `spec.maxReplicas` inside what the namespace's ResourceQuota allows; the
quota remains the hard ceiling on what any autoscaler — this one or an HPA —
can actually schedule.
