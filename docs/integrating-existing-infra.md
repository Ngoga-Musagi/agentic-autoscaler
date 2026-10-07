# Integrating with your existing infrastructure

The agentic autoscaler is **opt-in per Deployment** and reads its signals from
whatever Prometheus/Loki you already run. Nothing about your workloads changes —
you add one `AgenticAutoscaler` object that points at them. This guide covers
onboarding an existing Deployment (from the browser or with YAML) and driving
load against any service.

## 1. Install the operator

```sh
helm upgrade --install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  --set aiProvider.provider=anthropic \
  --set aiProvider.secretRef=ai-provider-secret
```

The operator watches **all namespaces** but only manages Deployments that have a
matching `AgenticAutoscaler` CR.

**Already running Prometheus/Loki?** Use the existing-infra overlay so the chart
doesn't assume a bundled stack and scopes egress to your monitoring namespace:

```sh
helm upgrade --install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  -f deploy/helm/values-existing-infra.yaml
```

### Validate your endpoints before onboarding (preflight)

Before creating a CR, test that the operator can actually reach your Prometheus
and Loki and that the queries return data — the most common reason a new CR
"doesn't scale" is an unreachable endpoint or a query that matches nothing. The
console exposes `POST /api/preflight` (write-mode) for this; the Onboard tab uses
it, or call it directly:

```sh
curl -s -X POST http://localhost:8090/api/preflight \
  -H 'Content-Type: application/json' \
  -d '{"prometheusURL":"http://prometheus-operated.monitoring:9090",
       "promQuery":"up{service=\"my-api\"}",
       "lokiURL":"http://loki.monitoring:3100",
       "lokiQuery":"{namespace=\"my-ns\", app=\"my-api\"}"}'
# → {"prometheus":{"checked":true,"ok":true,"results":N,...},
#    "loki":{"checked":true,"ok":true,"results":M,...}}
```

Each check reports `ok` plus a `detail` string ("reachable; N series",
"reachable, but no series — check the metric name / labels", "unreachable …").

## 2. Onboard a Deployment from the browser (no YAML)

The console can create the CR for you. It is **read-only by default** — enable
write mode first:

```sh
helm upgrade agentic-autoscaler deploy/helm/ -n agentic-autoscaler-system \
  --reuse-values --set console.writeEnabled=true \
  --set console.authSecretRef=console-token        # optional bearer-token gate
```

If you set `console.authSecretRef`, create the Secret out-of-band:

```sh
kubectl -n agentic-autoscaler-system create secret generic console-token \
  --from-literal=CONSOLE_AUTH_TOKEN="$(openssl rand -hex 16)"
```

Then port-forward and open the **Onboard** tab:

```sh
kubectl -n agentic-autoscaler-system port-forward \
  svc/agentic-autoscaler-agentic-autoscaler 8090:8090
# → http://localhost:8090  (Onboard tab)
```

Pick the namespace and Deployment, point at your Prometheus and Loki, set bounds,
choose a provider, leave **dry-run on**, and **Create**. The new autoscaler
appears under the **Autoscalers** tab.

> Security: `:8090` is unencrypted and the token is the only gate on cluster
> mutation. Keep it behind port-forward or an authenticated ingress — never
> expose it to the internet. See [.claude/rules/security.md](../.claude/rules/security.md).

## 3. Or onboard with YAML (GitOps)

Drop a CR next to your existing manifests. The operator reads signals from the
URLs you give it:

```yaml
apiVersion: scaling.autoscaler.io/v1alpha1
kind: AgenticAutoscaler
metadata:
  name: my-api-autoscaler
  namespace: my-namespace
spec:
  targetDeployment: my-api
  prometheusURL: "http://<your-prometheus>:9090"
  logSource:
    type: loki
    loki:
      url: "http://<your-loki>:3100"
      query: '{namespace="my-namespace", app="my-api"}'
  minReplicas: 2
  maxReplicas: 20
  cooldownSeconds: 120
  dryRun: true                       # observe for ~7 days, then go live
  aiProvider:
    provider: anthropic
    secretRef: ai-provider-secret
  hpaCoexistence:
    mode: owner                      # or calibrated, nested inside an existing HPA
  observability:
    grafanaURL: "http://<your-grafana>:3000"
    secretRef: grafana-api-secret
```

### Metric expectations (and how to adapt any service)

By default the signal collector (`pkg/signals/prometheus.go`) queries these
series for the target, labelled by service name:

- `http_requests_total{service="<deployment>"}` (rate + 5xx ratio)
- `http_request_duration_seconds_bucket{service="<deployment>"}` (p99 latency)
- `container_cpu_usage_seconds_total{pod=~"<deployment>-.*"}` (CPU)

Apps that export the Prometheus client defaults (many frameworks do — podinfo,
Go `promhttp`, etc.) work out of the box.

**If your service uses different metric names, override the queries per-CR** via
`spec.metrics` — no recording rules, no code changes. Each field is full PromQL;
the literal token `$TARGET` is replaced with the Deployment name at query time;
empty fields keep the default:

```yaml
spec:
  metrics:
    latencyP99:        'histogram_quantile(0.99, rate(myapp_request_seconds_bucket{app="$TARGET"}[2m]))'
    errorRate:         'sum(rate(myapp_requests_total{app="$TARGET",code=~"5.."}[2m])) / sum(rate(myapp_requests_total{app="$TARGET"}[2m])) * 100'
    cpuUtilization:    'avg(rate(container_cpu_usage_seconds_total{pod=~"$TARGET-.*"}[2m])) * 100'
    requestsPerSecond: 'sum(rate(myapp_requests_total{app="$TARGET"}[2m]))'
```

Contract for each query: `latencyP99` returns **seconds**, `errorRate` returns a
**percentage 0–100**, `cpuUtilization` returns a **percentage**, and
`requestsPerSecond` returns a **rate**. The Console **Onboard → Advanced — custom
metric queries** section exposes the same four fields with the defaults shown as
placeholders.

### Custom log patterns (match your own incident strings)

The operator ships with five built-in log patterns (`connection-pool-exhausted`,
`upstream-timeout`, `oom-killed`, `circuit-breaker-open`, `db-connection-failed`).
Real services phrase incidents differently, so you can add your own via
`spec.logPatterns` — no fork, no code change. Each entry is compiled and
**merged with the built-ins every reconcile**; an entry that reuses a built-in
name **overrides** it.

```yaml
spec:
  logPatterns:
    - name: tls-handshake-timeout          # new pattern, appended to the defaults
      regex: 'tls: handshake timeout'
      severity: critical                   # "warning" or "critical"
      score: 80                            # weight 0–100 toward the severity score
    - name: connection-pool-exhausted      # reuses a built-in name → overrides it
      regex: 'POOL DRAINED|pool at capacity'
      severity: critical
      score: 90
```

- `regex` is [RE2](https://github.com/google/re2/wiki/Syntax) syntax, matched
  against each raw log message.
- `score` is an integer **0–100** (mapped internally to 0.0–1.0); it sets the
  pattern's contribution to the fused `SeverityScore`.
- **Invalid entries are non-fatal.** A pattern whose regex fails to compile is
  skipped and reported in `status.logPatternWarnings`; the operator continues
  with the remaining valid patterns and all built-ins:

  ```sh
  kubectl get agenticautoscaler my-api-autoscaler \
    -o jsonpath='{.status.logPatternWarnings}'
  ```

To make a custom pattern drive the built-in scale-up rules, override a built-in
name the rules key on (e.g. `connection-pool-exhausted` feeds the rule that
scales up on pool exhaustion + high p99 latency).

### HPA coexistence

If an HPA already targets the Deployment, start in `calibrated` mode so the
agentic autoscaler operates **inside** the HPA's bounds (the HPA stays as a
safety net). Promote to `owner` only after the decision log looks correct. See
[.claude/rules/hpa-coexistence.md](../.claude/rules/hpa-coexistence.md).

## 4. Generate load against any service

The bundled load generator (demo/testing only) drives traffic at a service so
you can see autoscaling react. Point it at the sample app or your own:

```sh
make loadgen-deploy                                   # deploy the generator
make load-up LOAD_TARGET=http://my-api.my-namespace:8080 RPS=300
kubectl -n my-namespace get deploy my-api -w          # watch replicas climb
make load-down                                        # stop → scale back down
```

Plain RPS works against any HTTP service. The `ERRPCT=` and `DELAYMS=` knobs
assume a podinfo-style target that exposes `/status/{code}` and `/delay/{seconds}`
(used to drive the error-rate and latency rules deterministically).

Do not run the load generator in production, and only load-test services you are
authorised to.

### Watching it scale back down

After you stop the load, the operator releases the capacity it added — once all
signals have been healthy for the scale-down quiet window. That window defaults
to **15 minutes**; tune it per-CR with `spec.detection.scaleDownQuietWindowMinutes`
(minimum 1) if you want faster — or more conservative — scale-down:

```yaml
spec:
  detection:
    scaleDownQuietWindowMinutes: 10   # default 15
```

The scale-down is conservative by design: it releases to `minReplicas + 1` (one
replica of headroom above the floor), never below `minReplicas`.
