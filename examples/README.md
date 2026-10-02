# Example `AgenticAutoscaler` CRs

Three ready-to-adapt CRs covering the main ways the operator is used. Each opts a
single Deployment into agentic autoscaling — drop one next to your Deployment's
manifests. The operator only manages a Deployment that has a matching CR.

| File | Mode | Shows |
|------|------|-------|
| [`calibrated-coexistence.yaml`](calibrated-coexistence.yaml) | `calibrated` | runs **inside an existing HPA's bounds** (HPA stays as a safety net) — the recommended way to roll out |
| [`owner-mode.yaml`](owner-mode.yaml) | `owner` | agentic autoscaler is the **sole** controller of replicas (HPA deleted/suspended) |
| [`custom-service.yaml`](custom-service.yaml) | `owner` | a **non-podinfo** service: per-CR `spec.metrics` for different metric names, custom `spec.logPatterns`, and `spec.detection.consecutiveWindows` — proves the reusability story |

## Rollout discipline (applies to all)

1. Start with `dryRun: true` — the operator logs decisions and annotates Grafana but never changes replicas.
2. Review the decision log / Grafana annotations for ~7 days.
3. For an existing HPA, promote to `calibrated` first (HPA remains the safety net); only move to `owner` after 2+ weeks of correct decisions, then delete the HPA.

API keys for a cloud AI provider are **never** in the CR — they come from a
Kubernetes Secret referenced by the operator Deployment (see
`.claude/rules/security.md`). `spec.aiProvider.secretRef` only names the Secret.
