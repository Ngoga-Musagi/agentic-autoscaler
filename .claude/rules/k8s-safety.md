# Kubernetes safety rules

## Scale actions
- Never scale below `spec.minReplicas` — policy enforcer must check this before every action
- Never scale above `spec.maxReplicas` — same check
- Never execute a scale action if cooldown is active — check `status.lastScaleTime`
- Always write the decision to `status.lastDecisionReason` even when `dryRun: true`

## Dry-run discipline
- New deployments MUST start with `dryRun: true` for at least 7 days
- Disable dry-run only after reviewing the decision log in Grafana and confirming decisions are correct
- Set `dryRun: false` explicitly — never rely on the default

## HPA coexistence
- When `hpaCoexistence.enabled: true`, always read current HPA desired replicas before making a scale decision
- Never set `minReplicas` lower than HPA's minReplicas or `maxReplicas` higher than HPA's maxReplicas
- See .claude/skills/hpa-coexistence/SKILL.md for full rules

## Reconcile safety
- Every reconcile loop must complete in under 30 seconds total
- All external calls (Prometheus, Loki, LLM API) must have context timeouts
- If the signal collection or reasoning fails, requeue with backoff — never block indefinitely
- Never delete Kubernetes resources from within a reconcile loop without an explicit user-initiated action
