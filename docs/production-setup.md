# Production Setup Guide

This guide walks through installing the agentic-autoscaler operator in a production
cluster and rolling out autoscaling for an existing Deployment. The recommended path
is conservative: start in dry-run mode, observe for one week, then graduate to live
scaling in calibrated coexistence with your HPA before taking full ownership.

---

## Prerequisites

| Requirement | Minimum version | Notes |
|---|---|---|
| Kubernetes | 1.27 | Status subresource, server-side apply |
| Helm | 3.12 | For chart installation |
| Prometheus | 2.x | Reachable from the operator pod |
| Loki | 2.x or 3.x | At least one app namespace stream configured |
| Grafana | 9.x | Optional — enables decision annotation markers |

---

## Step 1 — Create the AI provider secret

The operator reads the API key from a Kubernetes Secret at startup. The Secret must
exist in the operator namespace before the Pod starts — missing it causes a crash loop.

**Anthropic (default):**

```bash
kubectl create secret generic ai-provider-secret \
  --from-literal=ANTHROPIC_API_KEY=<key> \
  -n agentic-autoscaler-system
```

**OpenAI:**

```bash
kubectl create secret generic ai-provider-secret \
  --from-literal=OPENAI_API_KEY=<key> \
  -n agentic-autoscaler-system
```

**Ollama (self-hosted — no secret required):**

When `aiProvider.provider=ollama`, no Secret is needed. The operator points to the
in-cluster Ollama service instead. See [Switching to Ollama](#switching-to-ollama)
below.

> **Security note:** Never commit API keys to Git or store them in `values.yaml`.
> In production, provision secrets from Vault, External Secrets Operator, or
> Sealed Secrets and point `aiProvider.secretRef` at the externally-managed Secret name.

---

## Step 2 — Create the Grafana secret

The operator pushes a vertical annotation to Grafana every time it executes a real
scale action. This shows on-call SREs exactly when and why a scale event happened.

```bash
kubectl create secret generic grafana-api-secret \
  --from-literal=GRAFANA_API_KEY=<key> \
  -n agentic-autoscaler-system
```

Skip this step and omit `grafana.url` from the Helm values if you are not using
Grafana. The operator starts and runs normally without it.

---

## Step 3 — Install the operator

Install into a dedicated namespace with `dryRun: true` (the chart default). The
operator will begin observing every opted-in Deployment and logging decisions, but
will not touch any replica counts until you explicitly disable dry-run.

```bash
helm install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system \
  --create-namespace \
  --set aiProvider.provider=anthropic \
  --set aiProvider.secretRef=ai-provider-secret \
  --set grafana.url=http://grafana.monitoring:3000 \
  --set grafana.secretRef=grafana-api-secret
```

Verify the operator is running:

```bash
kubectl -n agentic-autoscaler-system get pods
kubectl -n agentic-autoscaler-system logs -l app.kubernetes.io/name=agentic-autoscaler -f
```

The operator is healthy when you see repeated log lines similar to:

```
INFO  Reconcile complete  action=hold  deployment=payment-service  [dry-run]
```

### Switching to Ollama

To use a self-hosted Ollama model instead of a cloud API, enable the bundled
sub-chart and point the operator at it:

```bash
helm upgrade agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system \
  --set aiProvider.provider=ollama \
  --set ollama.enabled=true \
  --set ollama.model=llama3:8b
```

Ollama requires at least one node with 12 GB+ allocatable memory. The sub-chart
applies a `ollama=true:NoSchedule` toleration so Ollama Pods land on the dedicated
node pool without manual scheduling.

---

## Step 4 — Opt in a Deployment

The operator ignores every Deployment in the cluster unless there is a corresponding
`AgenticAutoscaler` CR. Opt in by creating the CR — no changes to the Deployment
itself are required.

A ready-to-use sample for a `payment-service` Deployment is at:

```
config/samples/payment-service-autoscaler.yaml
```

Apply it (adjust `namespace`, `prometheusURL`, and `logSource.loki.url` for your
cluster):

```bash
kubectl apply -f config/samples/payment-service-autoscaler.yaml
```

The sample ships with `dryRun: true` and `hpaCoexistence.mode: calibrated`. This is
the safest starting state — the operator runs alongside the existing HPA without
conflicting with it, and produces no scale actions until you remove the dry-run flag.

Confirm the CR was accepted and the first reconcile ran:

```bash
kubectl -n production get agenticautoscaler payment-service-autoscaler
kubectl -n production describe agenticautoscaler payment-service-autoscaler
```

The `Status` section shows the last decision and its reason:

```
Status:
  Current Replicas:       3
  Last Decision Reason:   hold — all signals nominal  [dry-run]
  HPA Coexistence Status: calibrated
```

---

## Step 5 — Rollout progression

Move through the three stages deliberately. Rushing past calibrated mode before
validating decisions has caused replica count oscillation in teams that skipped it.

### Stage 1 — Dry-run (minimum 7 days)

The default. Decisions are written to `status.lastDecisionReason` and the Grafana
decision log, but the replica count is never changed.

**What to watch:**

- Open the Grafana decision log (the `agentic-autoscaler-decisions` ConfigMap or
  the Grafana annotation timeline).
- Confirm that `scale-up` decisions correlate with real incidents (connection pool
  exhaustion, OOM events, elevated latency).
- Confirm that `scale-down` decisions appear only during genuine quiet periods.
- Flag any `scale-up` during low-traffic nights — this may indicate a misconfigured
  Loki query matching unrelated log streams.

**Checklist before advancing to Stage 2:**

- [ ] At least 7 days of dry-run decisions reviewed
- [ ] No false-positive scale-ups during known quiet windows
- [ ] Decision reasons are readable and reference specific log patterns or metrics
- [ ] `status.hpaCoexistenceStatus` shows `calibrated` (not `conflict-detected`)

### Stage 2 — Calibrated coexistence (minimum 14 days)

The operator executes real scale actions, but stays inside the HPA's replica
envelope. If the operator malfunctions, the HPA continues to function as a safety
net. Replica conflicts are avoided because the operator's `maxReplicas` is always
≤ the HPA's `maxReplicas`.

Enable live scaling by patching the CR:

```bash
kubectl -n production patch agenticautoscaler payment-service-autoscaler \
  --type=merge \
  -p '{"spec":{"dryRun":false}}'
```

**What to watch:**

- Grafana shows vertical annotation markers at every scale event.
- `kubectl -n production get agenticautoscaler` shows `Current Replicas` changing.
- Compare scale-event timing to your on-call alerts — the operator should act
  minutes before CPU-based HPA would fire.
- Check the cooldown is appropriate: if you see rapid oscillation, increase
  `cooldownSeconds`.

**Checklist before advancing to Stage 3:**

- [ ] At least 14 days of live calibrated scaling
- [ ] No `conflict-detected` status (would mean operator exceeded HPA bounds)
- [ ] At least one on-call incident where the operator scaled proactively and
      engineers confirmed the action was correct
- [ ] Cooldown period prevents oscillation during recovery

### Stage 3 — Owner mode (agentic autoscaler in full control)

The operator takes sole control of replica count. Delete or suspend the HPA first —
if both are active in owner mode, they fight over `spec.replicas`.

```bash
# 1. Delete (or suspend) the existing HPA
kubectl -n production delete hpa payment-service-hpa

# 2. Switch the CR to owner mode
kubectl -n production patch agenticautoscaler payment-service-autoscaler \
  --type=merge \
  -p '{"spec":{"hpaCoexistence":{"mode":"owner","hpaName":"","hpaNamespace":""}}}'
```

Confirm the status reflects the change:

```bash
kubectl -n production get agenticautoscaler payment-service-autoscaler \
  -o jsonpath='{.status.hpaCoexistenceStatus}'
# owner
```

**When to use owner mode:**

Owner mode is appropriate for deployments where the agentic autoscaler has been
running in calibrated mode for two or more weeks with consistently correct decisions.
Keep calibrated mode active for any deployment that:

- Has not yet had a real incident during the observation window
- Serves traffic from external partners with contractual SLOs
- Has unusual scaling patterns (e.g. batch jobs with spike traffic)

---

## Uninstalling

Removing the Helm release does not delete any `AgenticAutoscaler` CRs you have
created, nor does it delete the CRD (Helm never deletes CRDs on uninstall to
prevent accidental data loss).

```bash
# Remove the operator
helm uninstall agentic-autoscaler -n agentic-autoscaler-system

# Remove CRs for each opted-in deployment
kubectl delete agenticautoscaler --all -n production

# Remove the CRD (only after all CRs are deleted)
kubectl delete crd agenticautoscalers.scaling.autoscaler.io
```
