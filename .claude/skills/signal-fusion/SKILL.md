---
name: signal-fusion
description: Collecting Prometheus metrics and log streams, building SystemSnapshot, FusedSignal, log pattern matching. Use when working on pkg/signals or pkg/fusion, adding new metric queries, defining new log patterns, or debugging signal collection.
allowed-tools: Read, Grep, Glob
---

# Signal fusion

## Package constraint
`pkg/signals` and `pkg/fusion` must have **zero** `k8s.io` or `sigs.k8s.io` imports.
Pure Go + HTTP only. Enforced by the PreToolUse hook.

## Key types

```go
// SystemSnapshot — assembled by pkg/signals
type SystemSnapshot struct {
    Timestamp  time.Time
    Metrics    MetricSnapshot
    LogEntries []LogEntry
}

type MetricSnapshot struct {
    LatencyP99Ms   float64
    ErrorRatePct   float64
    CPUUtilPct     float64
    RequestsPerSec float64
    CustomMetrics  map[string]float64
}

// FusedSignal — produced by pkg/fusion
type FusedSignal struct {
    Snapshot        SystemSnapshot
    MatchedPatterns []PatternMatch
    SeverityScore   float64  // 0.0 healthy → 1.0 critical
    Recommendation  string   // "scale-up" | "scale-down" | "hold"
}

type PatternMatch struct {
    Name      string
    Severity  string  // "warning" | "critical"
    Count     int
    FirstSeen time.Time
}
```

## Prometheus queries (`pkg/signals/prometheus.go`)

```go
queries := map[string]string{
    "latency_p99": `histogram_quantile(0.99, rate(http_request_duration_seconds_bucket{service="%s"}[2m]))`,
    "error_rate":  `rate(http_requests_total{service="%s",status=~"5.."}[2m]) / rate(http_requests_total{service="%s"}[2m]) * 100`,
    "cpu_util":    `avg(rate(container_cpu_usage_seconds_total{pod=~"%s-.*"}[2m])) * 100`,
}
```

## Log patterns (`pkg/fusion/patterns.go`)

```go
var DefaultPatterns = []Pattern{
    {Name: "connection-pool-exhausted", Regex: `connection pool exhausted|POOL_EXHAUSTED`, Severity: "critical", Score: 0.8},
    {Name: "upstream-timeout",          Regex: `upstream timeout|ETIMEDOUT|context deadline exceeded`, Severity: "warning", Score: 0.5},
    {Name: "oom-killed",               Regex: `OOMKilled|out of memory`, Severity: "critical", Score: 0.9},
    {Name: "circuit-breaker-open",     Regex: `circuit.*open|CircuitBreaker.*OPEN`, Severity: "warning", Score: 0.6},
    {Name: "db-connection-failed",     Regex: `dial tcp.*connection refused|database.*unreachable`, Severity: "critical", Score: 0.7},
}
```

## Log source config in CRD spec

```yaml
logSource:
  type: "loki"   # or "kafka"
  loki:
    url: "http://loki.monitoring:3100"
    query: '{namespace="production", app="payment-service"}'
    lookbackMinutes: 5
  # kafka:
  #   brokers: ["kafka.infra:9092"]
  #   topic: "k8s-pod-logs"
  #   consumerGroup: "agentic-autoscaler"
```

## Testing signal packages

These packages are independently testable — no cluster needed:

```go
func TestCorrelator_DetectsConnectionPoolExhaustion(t *testing.T) {
    snapshot := SystemSnapshot{
        Metrics: MetricSnapshot{LatencyP99Ms: 2500, ErrorRatePct: 8},
        LogEntries: []LogEntry{{Message: "connection pool exhausted after 30s"}},
    }
    signal := correlator.Correlate(snapshot)
    assert.Equal(t, "scale-up", signal.Recommendation)
    assert.Contains(t, signal.PatternNames(), "connection-pool-exhausted")
}
```
