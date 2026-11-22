# Example `AgenticAutoscaler` CRs — one per service archetype

Copy the file closest to your service, change the names, apply it next to the
Deployment. The operator manages a Deployment **only** when a CR targets it.
Every file here is checked in CI: it must decode strictly into the API type
(`api/v1alpha1/examples_test.go`) and be accepted by a real API server running
the generated CRD (`internal/controller/examples_envtest_test.go`).

| File | Archetype | Shows |
|------|-----------|-------|
| [`http-api.yaml`](http-api.yaml) | Standard HTTP API | **zero overrides** — the default queries fit `http_*{service=…}` metrics |
| [`custom-metrics.yaml`](custom-metrics.yaml) | Own metric names | override the 4 PromQL queries via `spec.metrics` (`$TARGET` = Deployment name) |
| [`custom-logpatterns.yaml`](custom-logpatterns.yaml) | Bespoke failure logs | add patterns via `spec.logPatterns`; reuse a built-in name to drive the rules |
| [`queue-worker.yaml`](queue-worker.yaml) | Queue worker | map the slots onto jobs and scale on **backlog** (`spec.backlog`) |
| [`agent-runner.yaml`](agent-runner.yaml) | AI-agent workers | backlog + LLM rate-limit + token-budget signals (`spec.agentic`), agentic log patterns |
| [`advisor-mode.yaml`](advisor-mode.yaml) | Keep your HPA/KEDA | publish a recommendation; your autoscaler scales on max(CPU, agent) |
| [`calibrated-coexistence.yaml`](calibrated-coexistence.yaml) | HPA with no live scaling | operator writes replicas, clamped inside the HPA's min/max |
| [`owner-mode.yaml`](owner-mode.yaml) | No HPA | operator is the sole controller of replicas |
| [`custom-service.yaml`](custom-service.yaml) | Everything at once | metrics + patterns + the consecutive-window gate |

Live versions of these run in the multi-service demo: `config/samples/dev-*.yaml`
(see [docs/demo.md](../docs/demo.md)).

## Rollout discipline (applies to all)

1. **Shadow** — `dryRun: true` (the default in every file here): it decides and
   explains on Grafana and in the audit log, but never changes replicas. ≥ 7 days.
2. **Advisor** — `hpaCoexistence.mode: advisor`, `dryRun: false`: add its metric
   as one trigger on your existing HPA/KEDA. Your autoscaler stays the writer.
3. **Owner** (optional) — hand over `spec.replicas` once the decision log has
   earned trust. Still clamped to `minReplicas`/`maxReplicas` and the cooldown.

API keys for a cloud AI provider are **never** in the CR — they come from a
Kubernetes Secret referenced by the operator Deployment (see
`.claude/rules/security.md`). `spec.aiProvider.secretRef` only names the Secret.

See [docs/onboarding.md](../docs/onboarding.md) for the 5-minute walkthrough and
[docs/governance.md](../docs/governance.md) for the guardrails (`spec.policy`).
