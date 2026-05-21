# Agentic Autoscaler

Kubernetes-native operator that extends autoscaling beyond CPU/memory by fusing Prometheus metrics with log stream intelligence and AI reasoning to make proactive, explainable scaling decisions.

## Quick facts

- **Language**: Go 1.21+
- **Framework**: kubebuilder v3 / controller-runtime
- **Build**: `make generate && make manifests && make build`
- **Test**: `make test` (unit) · `make test-integration` (envtest)
- **Run locally**: `make run` against a `kind` cluster
- **Lint**: `golangci-lint run ./...`
- **Container**: `make docker-build IMG=<registry>/agentic-autoscaler:latest`

## Key directories

| Path | Purpose |
|------|---------|
| `api/v1alpha1/` | CRD type definitions — `AgenticAutoscalerSpec`, `AgenticAutoscalerStatus` |
| `internal/controller/` | Reconcile loop — wires all packages together |
| `pkg/signals/` | Prometheus + log collectors → `SystemSnapshot` |
| `pkg/fusion/` | Pattern correlation → `FusedSignal` |
| `pkg/reasoning/` | Rule-based + AI agent → `ScaleDecision` |
| `pkg/policy/` | Bounds enforcement + cooldown + HPA coordination |
| `pkg/scaler/` | Kubernetes API + KEDA execution |
| `pkg/observability/` | Grafana annotations + decision audit log |
| `config/` | Generated CRD, RBAC, manager manifests |
| `deploy/helm/` | Helm chart for cluster installation |

## Critical rules

- Every `Deployment` is opt-in: it must have an `AgenticAutoscaler` CR to be managed
- Never touch a `Deployment` that has no corresponding `AgenticAutoscaler` object
- `pkg/signals` and `pkg/fusion` must have zero Kubernetes dependencies — pure Go
- Always run `make generate` after changing types in `api/v1alpha1/`
- HPA coexistence: read `.claude/rules/hpa-coexistence.md` before touching `pkg/policy/`
- AI provider: Cloud (Anthropic/OpenAI) is default; Ollama self-hosted is a compile-time swap — read `.claude/rules/ai-provider.md`

## Imported context

@.claude/rules/code-style.md
@.claude/rules/hpa-coexistence.md
@.claude/rules/ai-provider.md
@.claude/rules/security.md

## Roadmap summary

Phase 1 Scaffold → Phase 2 CRD types → Phase 3 Signal layer → Phase 4 Reasoning engine → Phase 5 Controller wiring → Phase 6 Observability → Phase 7 Testing → Phase 8 Production rollout.

Full detail: `.claude/skills/operator-patterns/SKILL.md`
