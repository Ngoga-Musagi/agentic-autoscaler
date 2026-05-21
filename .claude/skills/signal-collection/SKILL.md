---
name: signal-collection
description: Prometheus metric queries, Loki log reading, Kafka log consumption, and SystemSnapshot assembly for the agentic-autoscaler. Use when working in pkg/signals or pkg/fusion, writing PromQL queries, implementing log pattern matching, or building the FusedSignal type.
allowed-tools: Read, Grep, Glob
---

# Signal collection

## Package boundaries

`pkg/signals` — zero Kubernetes imports. Pure Go + HTTP. Collects raw data.
`pkg/fusion`  — zero Kubernetes imports. Correlates signals into FusedSignal.

Keeping these packages free of Kubernetes dependencies makes them independently testable against real or mocked endpoints without a cluster.

## Prometheus collector

```go
// pkg/signals/prometheus.go

type MetricSnapshot struct {
    Timestamp        time.Time
    P99LatencyMS     float64
    ErrorRatePerSec  float64
    CPUUsagePercent  float64
    ActiveConnections float64
}

func (c *PrometheusCollector) Collect(ctx context.Context, cfg SignalConfig) (MetricSnapshot, error) {
    ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()

    // Use the Prometheus HTTP API client — not the Go client_golang scrape lib
    client, err := promapi.NewClient(promapi.Config{Address: c.prometheusURL})
    if err != nil { return MetricSnapshot{}, err }
    v1api := promv1.NewAPI(client)

    snap := MetricSnapshot{Timestamp: time.Now()}
    snap.P99LatencyMS, _ = c.queryScalar(ctx, v1api, cfg.LatencyQuery)
    snap.ErrorRatePerSec, _ = c.queryScalar(ctx, v1api, cfg.ErrorRateQuery)
    return snap, nil
}
```

## Loki log reader

```go
// pkg/signals/logreader.go

type LogEntry struct {
    Timestamp time.Time
    Line      string
    Labels    map[string]string
}

func (r *LokiReader) Recent(ctx context.Context, since time.Duration) ([]LogEntry, error) {
    end := time.Now()
    start := end.Add(-since)
    query := fmt.Sprintf(
        `{namespace=%q, app=%q}`,
        r.namespace, r.appLabel,
    )
    // Loki query_range HTTP call — returns streams of log lines
    url := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=500",
        r.lokiURL,
        neturl.QueryEscape(query),
        start.UnixNano(), end.UnixNano(),
    )
    // ... HTTP GET, parse JSON response into []LogEntry
}
```

## Kafka consumer (alternative to Loki)

```go
// pkg/signals/kafka_reader.go — only compiled when logSource.type == "kafka"

func (k *KafkaReader) Recent(ctx context.Context, since time.Duration) ([]LogEntry, error) {
    reader := kafka.NewReader(kafka.ReaderConfig{
        Brokers:  k.brokers,
        Topic:    k.topic,
        MinBytes: 1,
        MaxBytes: 10e6,
        MaxWait:  2 * time.Second,
    })
    defer reader.Close()
    // consume from offset corresponding to `since` duration
    // return assembled []LogEntry
}
```

## SystemSnapshot — the combined struct

```go
// pkg/signals/snapshot.go

type SystemSnapshot struct {
    Timestamp   time.Time
    Metrics     MetricSnapshot
    RecentLogs  []LogEntry
    WindowSize  time.Duration  // how far back logs were collected
}

func Assemble(metrics MetricSnapshot, logs []LogEntry, window time.Duration) SystemSnapshot {
    return SystemSnapshot{
        Timestamp:  time.Now(),
        Metrics:    metrics,
        RecentLogs: logs,
        WindowSize: window,
    }
}
```

## Fusion — pattern matching in pkg/fusion

```go
// pkg/fusion/correlator.go

type NamedPattern struct {
    Name     string
    Severity string   // "critical", "high", "medium"
    Matched  bool
    Count    int
}

type FusedSignal struct {
    Snapshot        signals.SystemSnapshot
    MatchedPatterns []NamedPattern
    AnomalyScore    float64   // 0.0–1.0, higher = more anomalous
}

func Correlate(snap signals.SystemSnapshot, patterns []PatternRule) FusedSignal {
    fused := FusedSignal{Snapshot: snap}
    for _, rule := range patterns {
        re := regexp.MustCompile(rule.Pattern)
        count := 0
        for _, entry := range snap.RecentLogs {
            if re.MatchString(entry.Line) { count++ }
        }
        if count > 0 {
            fused.MatchedPatterns = append(fused.MatchedPatterns, NamedPattern{
                Name: rule.Name, Severity: rule.Severity,
                Matched: true, Count: count,
            })
        }
    }
    fused.AnomalyScore = computeAnomalyScore(fused)
    return fused
}
```

## Timeouts — always set them

Every Prometheus and Loki call must have a context timeout of ≤10 seconds. The reconcile loop has a 30-second overall budget; network calls must not exhaust it.

## Anti-patterns

- Never query Prometheus inside a hot loop — one query per metric per reconcile
- Never collect more than 500 log lines per reconcile — use a 5-minute window
- Never panic on a failed HTTP call — return the error, let the reconciler requeue
- Never regex-compile inside a loop — compile patterns once at startup and cache them
