# Onboard any service in 5 minutes

The operator is service-agnostic: onboarding is one `AgenticAutoscaler` CR per
Deployment. No app changes, no fork. This page walks the decision; the
[`examples/`](../examples/README.md) catalog has a ready file per archetype.

## 1. Pick the archetype

| Your service | Start from |
|---|---|
| HTTP API exposing `http_request_duration_seconds` / `http_requests_total{service=…}` | `examples/http-api.yaml` |
| HTTP/gRPC API with its own metric names | `examples/custom-metrics.yaml` |
| Queue / stream worker | `examples/queue-worker.yaml` |
| AI-agent worker (calls an LLM + tools) | `examples/agent-runner.yaml` |
| Already scaled by an HPA or KEDA | add `hpaCoexistence.mode: advisor` (`examples/advisor-mode.yaml`) |

## 2. Point it at your signals

**Metrics.** The operator reads four numbers per reconcile: p99 latency, error
rate, CPU, request rate. Each is a PromQL query in which `$TARGET` becomes
`spec.targetDeployment`. Override only the ones whose names differ:

```yaml
spec:
  metrics:
    latencyP99:        'histogram_quantile(0.99, sum by (le) (rate(shop_request_seconds_bucket{app="$TARGET"}[2m]))) >= 0'  # seconds
    errorRate:         'sum(rate(shop_requests_total{app="$TARGET",code=~"5.."}[2m])) / (sum(rate(shop_requests_total{app="$TARGET"}[2m])) > 0) * 100'
    requestsPerSecond: 'sum(rate(shop_requests_total{app="$TARGET"}[2m]))'
```

Check each query in Prometheus first, with `$TARGET` replaced by hand. The
`>= 0` and `> 0` guards make an idle service report "no data" (read as 0)
instead of NaN. Or use the console's **Onboard** tab: its preflight runs the
queries against your Prometheus/Loki before creating the CR.

**Logs.** Point `spec.logSource.loki.query` at the service's streams. Five
incident patterns are built in; add your own with `spec.logPatterns`
(`score` is an integer 0–100). Raw log lines never leave the operator — only
pattern names and counts reach the LLM.

## 3. Bounds and safety

Set `minReplicas`, `maxReplicas` and `cooldownSeconds` — the policy layer clamps
every decision to them, whatever the reasoning engine says. Optionally cap the
step size, set a confidence floor or an approval threshold with `spec.policy`
([docs/governance.md](governance.md)).

## 4. Roll out

`dryRun: true` → review a week of decisions on the *Agentic Autoscaler — Decision
Audit* dashboard → `advisor` mode on your existing HPA/KEDA → optionally `owner`.

## Proven on the demo

`deploy/checkout` runs a service whose metrics are `shop_request_seconds` /
`shop_requests_total{app,code}` — names the operator's defaults would never
find. `config/samples/dev-checkout-autoscaler.yaml` onboards it with only
`spec.metrics` overrides, and the unchanged operator scales it during a timeout
storm (`bash hack/demo.sh checkout-storm-up`).
