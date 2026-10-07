# Timeout-storm demo — log-pattern-driven scaling

This is the project's flagship demo: the operator scales a workload because a
**log pattern** (`connection pool exhausted`) is fused with a **Prometheus p99
latency spike** — firing rule 2 — rather than reacting to CPU or error rate. It
is the mechanism the poster is about, shown end to end on a laptop `kind`
cluster.

## What drives it

`cmd/faultgen` is an independent demo workload (never the operator faking logs).
When its fault mode is on it does two things at once:

1. injects **> 2 s** response latency, so p99 crosses rule 2's 2000 ms threshold, and
2. writes `connection pool exhausted …` lines to **stdout**, which Promtail ships
   to Loki.

The operator then queries Prometheus (p99) and Loki (the log pattern), fuses
them, and rule 2 fires. `cmd/faultgen/emit_test.go` pins the contract that every
emitted line matches a `pkg/fusion` pattern, so the demo can never drift into
emitting lines the operator would not detect.

## Run it

```sh
# Base stack (kind + Prometheus/Loki/Grafana + operator):
bash hack/bootstrap.sh

# Deploy faultgen + its autoscaler + the load generator:
make demo-timeout-storm

# Inject the storm (all replicas fault; ~100 rps of traffic):
make storm-up

# Watch the decision appear (~2 min for the p99 [2m] window to fill):
kubectl -n production get agenticautoscaler faultgen-autoscaler \
  -o jsonpath='{.status.lastDecisionReason}{"\n"}'

# Clear it:
make storm-down
```

The autoscaler starts in **dry-run** (safe): it records the scale-up decision
and the matched pattern without changing replicas. To see it scale for real:

```sh
make storm-live      # sets dryRun=false
make storm-up
kubectl -n production get deploy faultgen -w   # replicas climb 2 → 3 → …
```

## What you should see

The decision is driven by the log pattern, not error rate — the audit record
carries the matched pattern:

```jsonc
{
  "action": "scale-up",
  "reason": "connection pool exhausted with p99 latency spike (replicas 2 → 3)",
  "patternsMatched": ["connection-pool-exhausted"],
  "provider": "rule-based",
  "oldReplicas": 2,
  "newReplicas": 3,
  "dryRun": true
}
```

Confirm each stage independently:

```sh
# Prometheus: aggregated p99 for faultgen is > 2 s during the storm
#   histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{service="faultgen"}[2m])))
# Loki: the pattern lines are being ingested
#   {namespace="production", app="faultgen"}
```

> Notes
> - faultgen runs multiple replicas, so the CR overrides the p99/error queries to
>   aggregate across pods (`sum by (le) (...)`); the per-pod defaults would return
>   one series per pod and the operator reads the first.
> - `faultgen` exposes metrics labelled `service="faultgen"` (= the CR's
>   `$TARGET`) and is scraped by a `ServiceMonitor` (kube-prometheus-stack does
>   not use `prometheus.io/scrape` annotations).
> - Demo/testing only. Never run faultgen or the load generator in production.

## Watching it scale back down

Scaling up is only half the story — the operator also releases capacity when
things go quiet (rule 5: zero severity + CPU < 20% sustained for the quiet
window). The window defaults to **15 minutes**, which is a long demo, so make it
short with `spec.detection.scaleDownQuietWindowMinutes`:

```sh
make demo-scale-down QUIET=2     # faultgen: live, owner, 2-min quiet window
make storm-up                    # drives it UP (rule 2)
make storm-down                  # clears the storm → signals go quiet
kubectl -n production get deploy faultgen -w   # ~2 min later: back down to min+1
```

The scale-down decision is recorded like any other:

```jsonc
{ "action": "scale-down",
  "reason": "all signals healthy for the quiet window, releasing excess capacity (replicas 8 → 3)",
  "provider": "rule-based" }
```

In production leave the window at its conservative default (or higher); the short
window is only to make the demo observable in minutes rather than a quarter hour.
