# AI provider rules

The reasoning engine supports two providers. The choice is set at operator startup via environment variable — no recompilation needed.

## Provider selection

```bash
# Cloud — Anthropic Claude (default)
AI_PROVIDER=anthropic
ANTHROPIC_API_KEY=<from-k8s-secret>
ANTHROPIC_MODEL=claude-sonnet-4-20250514

# Cloud — OpenAI
AI_PROVIDER=openai
OPENAI_API_KEY=<from-k8s-secret>
OPENAI_MODEL=gpt-4o

# Self-hosted — Ollama (in-cluster)
AI_PROVIDER=ollama
OLLAMA_BASE_URL=http://ollama.ollama.svc.cluster.local:11434
OLLAMA_MODEL=llama3:8b
```

## Architecture rule
`pkg/reasoning/agent.go` exposes a single interface:

```go
type Agent interface {
    Decide(ctx context.Context, signal fusion.FusedSignal) (ScaleDecision, error)
}
```

`NewAgent(cfg Config) Agent` returns the correct implementation based on `cfg.Provider`.
Never call provider SDKs directly from `internal/controller/` — always go through this interface.

## Cloud provider (Anthropic / OpenAI)
- HTTP timeout: 10 seconds
- Retry: 2 retries with 500ms backoff on 429 or 5xx
- API key sourced from env var, which is mounted from a K8s Secret
- Fallback: if the cloud API fails after retries, fall through to the rule-based agent

## Ollama self-hosted
- Deploy Ollama as a `Deployment` in the `ollama` namespace using `deploy/helm/charts/ollama/`
- Recommended models: `llama3:8b` (8GB RAM) or `mistral:7b` (8GB RAM)
- Node requirement: at least one node with 12GB+ allocatable memory
- The Ollama pod must be on a dedicated node pool (taint: `ollama=true:NoSchedule`)
- HTTP timeout: 30 seconds (local inference is slower than cloud API)
- No retry on timeout — return rule-based fallback immediately

## Rule-based fallback (always available)
The `RuleBasedAgent` in `pkg/reasoning/rules.go` handles deterministic cases without any LLM:
- Connection pool exhausted AND latency p99 > 2s → scale up 50%
- Error rate > 10% for 5+ minutes → scale up 30%
- All signals green for 15+ minutes after scale-up → scale down to baseline
- Any other pattern → return `ScaleDecision{Action: "hold"}`

The rule-based agent is the last line of defense if the AI provider is unavailable.
