---
name: observability
description: Grafana annotation API, decision audit log, explainability for SREs, Prometheus metrics exposition from the operator itself. Use when working on pkg/observability, adding new decision metrics, debugging Grafana annotation failures, or building audit queries.
allowed-tools: Read, Grep, Glob
---

# Observability

## What gets recorded per ScaleDecision

Every `ScaleDecision` — including "hold" decisions in dry-run mode — is recorded with:

```go
type DecisionRecord struct {
    Timestamp      time.Time
    Deployment     string
    Namespace      string
    OldReplicas    int32
    NewReplicas    int32
    Action         string   // "scale-up" | "scale-down" | "hold"
    Reason         string   // AI-generated natural language explanation
    Confidence     float64
    Provider       string   // "anthropic" | "openai" | "ollama" | "rule-based"
    PatternsMatched []string
    DryRun         bool
}
```

## Grafana annotation (`pkg/observability/annotations.go`)

Pushes a visual marker to Grafana at the exact moment a scale event happens.
SREs see this as a vertical line on any dashboard with a tooltip showing the AI explanation.

```go
func (g *GrafanaClient) PushAnnotation(ctx context.Context, r DecisionRecord) error {
    body := map[string]interface{}{
        "time":    r.Timestamp.UnixMilli(),
        "tags":    []string{"agentic-autoscaler", r.Deployment, r.Action},
        "text":    fmt.Sprintf("[%s] %s: %s → %d replicas. %s", r.Action, r.Deployment, r.OldReplicas, r.NewReplicas, r.Reason),
        "dashboardUID": g.cfg.DashboardUID,
    }
    // POST to /api/annotations with Bearer token from K8s Secret
}
```

Grafana URL and API key are set via env vars:
```
GRAFANA_URL=http://grafana.monitoring:3000
GRAFANA_API_KEY=<from-k8s-secret>
GRAFANA_DASHBOARD_UID=<optional, annotates all dashboards if empty>
```

## Audit log (`pkg/observability/explainer.go`)

Persists all `DecisionRecord` objects to a `ConfigMap` (simple) or an append-only store.

```go
// Simple ConfigMap approach (good for < 500 records)
func (e *Explainer) Record(ctx context.Context, r DecisionRecord) error {
    // append JSON to ConfigMap "decision-audit-log" in operator namespace
    // rotate when > 100 entries
}
```

For production scale: write to a PostgreSQL sidecar or emit as structured log lines
that Loki indexes — then query with LogQL from Grafana.

## Operator's own Prometheus metrics

Expose these from `pkg/observability/metrics.go` via the controller-runtime metrics endpoint:

```go
var (
    ScaleDecisionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "agentic_autoscaler_decisions_total",
        Help: "Total scale decisions by action and deployment",
    }, []string{"action", "deployment", "provider"})

    DecisionLatencySeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "agentic_autoscaler_decision_latency_seconds",
        Help:    "Time from signal collection to scale execution",
        Buckets: prometheus.DefBuckets,
    }, []string{"deployment"})

    AIProviderErrors = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "agentic_autoscaler_ai_provider_errors_total",
        Help: "AI provider call failures (falls back to rule-based)",
    }, []string{"provider"})
)
```

These are scraped by your existing Prometheus — no additional scrape config needed
(controller-runtime exposes `/metrics` on port 8080 by default).
