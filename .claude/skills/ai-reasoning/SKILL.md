---
name: ai-reasoning
description: AI agent interface, ScaleDecision type, rule-based fallback agent, Anthropic/OpenAI cloud provider, Ollama self-hosted provider, prompt construction, provider switching. Use when working on pkg/reasoning, adding new AI providers, tuning the prompt, or debugging scale decisions.
allowed-tools: Read, Grep, Glob
---

# AI reasoning

## The Agent interface

```go
// pkg/reasoning/agent.go
type Agent interface {
    Decide(ctx context.Context, signal fusion.FusedSignal) (ScaleDecision, error)
}

type ScaleDecision struct {
    Action       string       // "scale-up" | "scale-down" | "hold"
    TargetReplicas int32
    Confidence   float64      // 0.0–1.0
    Reason       string       // natural language explanation for SREs
    Timestamp    *metav1.Time
}

func NewAgent(cfg Config) Agent {
    switch cfg.Provider {
    case "anthropic": return NewAnthropicAgent(cfg)
    case "openai":    return NewOpenAIAgent(cfg)
    case "ollama":    return NewOllamaAgent(cfg)
    default:          return NewRuleBasedAgent()
    }
}
```

## Provider config via env vars

| Provider | Env vars required |
|----------|-------------------|
| `anthropic` | `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL` |
| `openai` | `OPENAI_API_KEY`, `OPENAI_MODEL` |
| `ollama` | `OLLAMA_BASE_URL`, `OLLAMA_MODEL` |

All sourced from a K8s Secret — never hardcoded. See `.claude/rules/ai-provider.md`.

## Prompt structure (`pkg/reasoning/prompt.go`)

The prompt instructs the model to return structured JSON only:

```
System: You are a Kubernetes autoscaling agent. Analyze the system state and return a JSON ScaleDecision.
        Respond ONLY with valid JSON. No explanation outside the JSON.

User:
Current replicas: {{.CurrentReplicas}}
Min: {{.MinReplicas}} Max: {{.MaxReplicas}}

Metrics (last 5 minutes):
- Latency p99: {{.LatencyP99Ms}}ms
- Error rate: {{.ErrorRatePct}}%
- CPU util: {{.CPUUtilPct}}%

Log patterns matched: {{range .Patterns}}- {{.Name}} ({{.Severity}}): {{.Count}} occurrences{{end}}

Respond with: {"action":"scale-up|scale-down|hold","targetReplicas":N,"confidence":0.0-1.0,"reason":"..."}
```

## Ollama deployment (self-hosted path)

```yaml
# deploy/helm/charts/ollama/values.yaml
ollama:
  enabled: false          # set true to deploy in-cluster
  model: "llama3:8b"
  nodeSelector:
    role: ollama
  tolerations:
    - key: ollama
      operator: Equal
      value: "true"
      effect: NoSchedule
  resources:
    requests:
      memory: "10Gi"
    limits:
      memory: "12Gi"
```

## Rule-based fallback (always active)

```go
// pkg/reasoning/rules.go — no LLM call
func (r *RuleBasedAgent) Decide(_ context.Context, s fusion.FusedSignal) (ScaleDecision, error) {
    if s.HasPattern("connection-pool-exhausted") && s.Metrics.LatencyP99Ms > 2000 {
        return scaleUp(s, 0.5, "connection pool exhausted + latency spike")
    }
    if s.Metrics.ErrorRatePct > 10 {
        return scaleUp(s, 0.3, "error rate exceeded 10%")
    }
    return hold("no action threshold met")
}
```
