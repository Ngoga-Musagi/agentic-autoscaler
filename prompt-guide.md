# Agentic Autoscaler — Claude Code Prompt Guide

This guide gives you the exact prompts to use in **Claude Code** (`claude` CLI), ordered by
project phase. Each prompt is self-contained: copy it, paste it into the terminal where Claude
Code is running inside your project root, and follow the output.

> **Before you start**: open a terminal, `cd agentic-autoscaler`, and run `claude` to start
> Claude Code with this project loaded. CLAUDE.md loads automatically on session start.

---

## How to use this guide

- Prompts marked **[PROMPT]** are copy-paste ready.
- Prompts marked **[VERIFY]** are checks you run yourself or ask Claude to run for you.
- Prompts marked **[DECISION]** require you to make a choice before continuing.
- Each phase ends with a gate — do not move to the next phase until the gate passes.

---

## Phase 1 — Scaffold the Go project

**Goal**: generate the kubebuilder skeleton so you have a compilable project before writing
any business logic.

---

### 1.1 — Initialize the module

```
/onboard Initialize the kubebuilder project scaffold for the agentic-autoscaler operator
```

Then:

```
Use kubebuilder to scaffold this Go operator project. Run the following commands in order:

1. kubebuilder init --domain autoscaler.io --repo github.com/yourorg/agentic-autoscaler
2. kubebuilder create api --group scaling --version v1alpha1 --kind AgenticAutoscaler --resource --controller

After each command succeeds, show me the new files that were created and explain what each one does.
Do not modify any generated files yet — just confirm the scaffold compiles with: go build ./...
```

### 1.2 — Verify scaffold compiles

**[VERIFY]**
```
Run: go build ./...
If it fails, show me the error and fix it before moving on.
Also run: make manifests
Show me what files were generated under config/
```

### 1.3 — Set up the kind cluster

```
Help me create a local kind cluster for development. Run:

1. kind create cluster --name agentic-autoscaler-dev
2. kubectl cluster-info --context kind-agentic-autoscaler-dev
3. Confirm kubectl is pointing to the kind cluster

Then install the CRDs (even though they are empty stubs at this point):
make install
```

**Phase 1 gate**: `go build ./...` passes. `kubectl get crd | grep agenticautoscaler` returns one result.

---

## Phase 2 — Define CRD types

**Goal**: `api/v1alpha1/types.go` fully reflects the spec we designed — including opt-in model,
HPA coexistence fields, AI provider config, and dry-run flag.

---

### 2.1 — Write the Spec and Status structs

```
Read .claude/skills/operator-patterns/SKILL.md and .claude/rules/hpa-coexistence.md.

Then open api/v1alpha1/agentic_autoscaler_types.go and replace the stub Spec and Status
structs with the full implementation. The Spec must include:

- targetDeployment (string, required)
- namespace (string, optional — defaults to metadata.namespace)
- prometheusURL (string, required)
- logSource (LogSourceConfig struct — supports type "loki" or "kafka" with nested config)
- minReplicas (int32, required)
- maxReplicas (int32, required)
- cooldownSeconds (int32, required)
- dryRun (bool, optional, default false)
- aiProvider (AIProviderConfig struct — provider string + secretRef string)
- hpaCoexistence (HPACoexistence struct — mode string + hpaName string + hpaNamespace string)
- observability (ObservabilityConfig struct — grafana URL + secretRef)

The Status must include:
- currentReplicas (int32)
- lastScaleTime (*metav1.Time)
- lastDecisionReason (string)
- hpaCoexistenceStatus (string)
- observedGeneration (int64)

Add all necessary +kubebuilder:rbac markers for:
- agenticautoscalers (get, list, watch, create, update, patch, delete)
- agenticautoscalers/status (get, update, patch)
- deployments (get, list, watch, update, patch)
- horizontalpodautoscalers (get, list, watch — READ ONLY)

After writing the types, run: make generate && make manifests
Show me the generated CRD YAML to confirm all fields appear correctly.
```

### 2.2 — Write a validation test for the types

```
Write a test file api/v1alpha1/types_test.go that:

1. Creates a sample AgenticAutoscaler object in Go (not YAML)
2. Marshals it to JSON and back
3. Asserts all fields survive the round-trip correctly

Also load and parse config/samples/payment-service-autoscaler.yaml into an
AgenticAutoscaler struct and assert it deserializes without error.

Run: go test ./api/...
```

**Phase 2 gate**: `make generate && make manifests` runs clean. `go test ./api/...` passes.
The CRD YAML under `config/crd/bases/` contains `targetDeployment`, `hpaCoexistence`, and `dryRun` fields.

---

## Phase 3 — Build the signal layer

**Goal**: `pkg/signals` fetches real data from Prometheus and Loki/Kafka.
`pkg/fusion` correlates it into a `FusedSignal`. No Kubernetes imports in either package.

---

### 3.1 — Build the Prometheus collector

```
Read .claude/skills/signal-fusion/SKILL.md.

Create pkg/signals/prometheus.go with a PrometheusCollector struct that:

1. Takes a PrometheusURL string in its constructor
2. Has a Collect(ctx context.Context, targetDeployment string) (MetricSnapshot, error) method
3. Runs these PromQL queries via the Prometheus HTTP API (/api/v1/query):
   - http request latency p99 (seconds, converted to ms)
   - HTTP error rate percentage (5xx / total * 100)
   - CPU utilization percentage
   - Requests per second
4. Uses a 10-second HTTP timeout
5. Returns a MetricSnapshot struct defined in pkg/signals/snapshot.go

Also create pkg/signals/snapshot.go with the SystemSnapshot, MetricSnapshot, and LogEntry types.

Write a test in pkg/signals/prometheus_test.go that uses httptest.NewServer to mock
the Prometheus API and asserts the correct PromQL queries are sent and MetricSnapshot
fields are populated.

Run: go test ./pkg/signals/...
```

### 3.2 — Build the log reader

**[DECISION]** Which log backend are you using first?

**If Loki:**
```
Create pkg/signals/logreader.go with a LokiReader struct that:

1. Takes a LokiURL and a LogQL query string in its constructor
2. Has a Read(ctx context.Context, lookbackMinutes int) ([]LogEntry, error) method
3. Calls the Loki query_range API: GET /loki/api/v1/query_range
4. Parses the response into []LogEntry (each with Timestamp, Level, Message, Labels)
5. Uses a 10-second HTTP timeout
6. Sanitizes log lines: strips ANSI escape codes, truncates each message to 2000 chars

Write a test using httptest.NewServer that mocks the Loki response format.
Run: go test ./pkg/signals/...
```

**If Kafka:**
```
Create pkg/signals/kafkareader.go with a KafkaReader struct using github.com/segmentio/kafka-go that:

1. Takes brokers []string, topic string, consumerGroup string in its constructor
2. Has a Read(ctx context.Context, lookbackMinutes int) ([]LogEntry, error) method
3. Reads messages from the last N minutes by seeking to the appropriate offset
4. Parses each message as JSON into LogEntry
5. Sanitizes: strip ANSI codes, truncate to 2000 chars

Run: go get github.com/segmentio/kafka-go
Run: go test ./pkg/signals/...
```

### 3.3 — Build the signal fusion correlator

```
Read .claude/skills/signal-fusion/SKILL.md for the FusedSignal type and DefaultPatterns list.

Create the following files:

1. pkg/fusion/types.go — FusedSignal, PatternMatch structs
2. pkg/fusion/patterns.go — DefaultPatterns slice with the 5 named patterns:
   - connection-pool-exhausted (critical, 0.8)
   - upstream-timeout (warning, 0.5)
   - oom-killed (critical, 0.9)
   - circuit-breaker-open (warning, 0.6)
   - db-connection-failed (critical, 0.7)
3. pkg/fusion/correlator.go — Correlate(snapshot signals.SystemSnapshot) FusedSignal function that:
   - Matches each DefaultPattern against all log entries
   - Computes a composite SeverityScore (max of matched pattern scores, capped at 1.0)
   - Sets Recommendation to "scale-up" if SeverityScore > 0.5 and latency > 1000ms
   - Sets Recommendation to "scale-down" if SeverityScore == 0.0 and CPU < 20% for 10+ minutes
   - Otherwise "hold"

Write table-driven tests in pkg/fusion/correlator_test.go covering:
- A snapshot with connection-pool-exhausted log + high latency → scale-up
- A snapshot with no patterns + low CPU → scale-down
- A snapshot with warning patterns but normal latency → hold

Run: go test ./pkg/fusion/...
Confirm: grep -r "k8s.io\|sigs.k8s.io" pkg/signals/ pkg/fusion/ returns nothing
```

**Phase 3 gate**: `go test ./pkg/signals/... ./pkg/fusion/...` passes.
`grep -r "k8s.io\|sigs.k8s.io" pkg/signals/ pkg/fusion/` returns **zero** results.

---

## Phase 4 — Build the reasoning engine

**Goal**: `pkg/reasoning` has three agent implementations behind one interface. Rule-based works
offline. Cloud AI calls Anthropic. Ollama option is wired and configurable.

---

### 4.1 — Define the Agent interface and ScaleDecision type

```
Read .claude/skills/ai-reasoning/SKILL.md and .claude/rules/ai-provider.md.

Create pkg/reasoning/agent.go with:

1. The Agent interface: Decide(ctx context.Context, signal fusion.FusedSignal) (ScaleDecision, error)
2. The ScaleDecision struct with: Action, TargetReplicas, Confidence, Reason, Timestamp
3. The Config struct with: Provider string, APIKey string, Model string, BaseURL string,
   CurrentReplicas int32, MinReplicas int32, MaxReplicas int32
4. The NewAgent(cfg Config) Agent factory function that switches on cfg.Provider
5. Helper functions: scaleUpByFraction, scaleDownToBaseline, hold

Also create pkg/reasoning/decision.go with helper constructors:
- HoldDecision(reason string) ScaleDecision
- ScaleUpDecision(current, target int32, confidence float64, reason string) ScaleDecision
- ScaleDownDecision(current, target int32, confidence float64, reason string) ScaleDecision
```

### 4.2 — Build the rule-based agent (works with no API key)

```
Create pkg/reasoning/rules.go with a RuleBasedAgent struct implementing Agent.

The Decide method must handle these cases in priority order:
1. oom-killed pattern → scale up 50%, confidence 0.95, reason explains OOM
2. connection-pool-exhausted AND latency > 2000ms → scale up 50%, confidence 0.9
3. error rate > 10% for sustained period → scale up 30%, confidence 0.8
4. circuit-breaker-open → scale up 30%, confidence 0.7
5. SeverityScore == 0.0 AND CPU < 20% AND no patterns for 15+ min → scale down to min+1
6. Default → hold

Write table-driven tests in pkg/reasoning/rules_test.go with 8+ scenarios.
Run: go test ./pkg/reasoning/...

This agent must work with no network — it is the fallback for when the AI provider fails.
```

### 4.3 — Build the Anthropic cloud agent

```
Read .claude/rules/ai-provider.md and .claude/rules/security.md.

Create pkg/reasoning/prompt.go with BuildPrompt(cfg Config, signal fusion.FusedSignal) string.
The prompt must:
- Instruct the model to return ONLY valid JSON (no prose outside the JSON)
- Include: deployment name, current/min/max replicas, all metric values, matched patterns
- Request this exact JSON shape: {"action":"...","targetReplicas":N,"confidence":0.0,"reason":"..."}

Create pkg/reasoning/anthropic.go with AnthropicAgent:
- HTTP POST to https://api.anthropic.com/v1/messages
- Model from cfg.Model (default: claude-sonnet-4-20250514)
- API key from cfg.APIKey (sourced from env at startup — never hardcoded)
- 10-second timeout
- 2 retries with 500ms backoff on 429 or 5xx
- Parse JSON response into ScaleDecision
- On any failure after retries: log a warning and fall through to RuleBasedAgent.Decide()

Write a test using httptest.NewServer that:
- Mocks a successful Anthropic response with valid JSON
- Mocks a 429 response and verifies retry behavior
- Mocks a timeout and verifies fallback to rule-based decision

Run: go test ./pkg/reasoning/...
```

### 4.4 — Build the Ollama agent (self-hosted path)

```
Read .claude/rules/ai-provider.md for Ollama deployment requirements.

Create pkg/reasoning/ollama.go with OllamaAgent:
- HTTP POST to cfg.BaseURL + /api/generate (Ollama's generate endpoint)
- Model from cfg.Model (e.g. "llama3:8b")
- 30-second timeout (local inference is slower than cloud API)
- Same prompt from prompt.go
- Parse JSON response — Ollama wraps it in a "response" field, extract and parse
- On failure: fall through to RuleBasedAgent.Decide() immediately (no retries on timeout)

The NewAgent factory should pick OllamaAgent when AI_PROVIDER=ollama.

Write a test using httptest.NewServer mocking the Ollama response format.
Run: go test ./pkg/reasoning/...
```

### 4.5 — Wire provider selection via environment variable

```
Update pkg/reasoning/agent.go so NewAgent reads these env vars:
- AI_PROVIDER (default: "rules" — rule-based only, safe default)
- ANTHROPIC_API_KEY or OPENAI_API_KEY or OLLAMA_BASE_URL depending on provider
- ANTHROPIC_MODEL / OPENAI_MODEL / OLLAMA_MODEL

Write a test that constructs agents for each provider type using env var stubs.
Run: go test ./pkg/reasoning/...
```

**Phase 4 gate**: `go test ./pkg/reasoning/...` passes for all three agent types.
Rule-based works with zero network. Anthropic and Ollama agents fall back to rule-based on failure.

---

## Phase 5 — Build policy, scaler, and wire the controller

**Goal**: a fully working operator that reconciles `AgenticAutoscaler` objects on a `kind` cluster.

---

### 5.1 — Build the policy enforcer

```
Read .claude/rules/hpa-coexistence.md carefully before writing anything.

Create the following files:

1. pkg/policy/enforcer.go with Enforcer.Enforce(ctx, spec, decision) (ScaleDecision, error):
   - Clamp TargetReplicas to [spec.MinReplicas, spec.MaxReplicas]
   - Check cooldown: if time since lastScaleTime < cooldownSeconds, return hold decision
   - If hpaCoexistence.mode == "calibrated": fetch the named HPA, clamp within its bounds
   - Log a warning if clamping changed the target
   - Never modify or delete the HPA object — read only

2. pkg/policy/cooldown.go with CooldownChecker — separated for testability

Write tests in pkg/policy/enforcer_test.go covering:
- A decision within bounds passes through unchanged
- A decision above maxReplicas is clamped to maxReplicas
- A decision within cooldown window becomes a hold
- Calibrated mode clamps to HPA bounds

Run: go test ./pkg/policy/...
```

### 5.2 — Build the scaler executor

```
Read .claude/skills/kubernetes-api/SKILL.md.

Create pkg/scaler/executor.go with Executor.Execute(ctx, spec, decision) error:
- Uses strategic merge patch to set spec.replicas: client.RawPatch(types.MergePatchType, ...)
- Logs the old replica count, new replica count, and the decision reason
- Returns a wrapped error on failure

Create pkg/scaler/keda.go with KEDAExecutor.Execute — creates or patches a
KEDA ScaledObject instead of patching replicas directly. This is the alternative
execution path when spec.scaler.type == "keda".

Run: go test ./pkg/scaler/... (use envtest for these tests — see Phase 7)
```

### 5.3 — Wire the Reconcile loop

```
Read .claude/skills/operator-patterns/SKILL.md for the full Reconcile skeleton.

Open internal/controller/agentic_autoscaler_controller.go and implement the full
Reconcile() function following the skeleton in the skill document:

1. Fetch the AgenticAutoscaler CR — return nil if NotFound
2. Collect signals (PrometheusCollector + LogReader)
3. Fuse signals (fusion.Correlate)
4. Call the reasoning agent (pkg/reasoning.NewAgent from env vars)
5. Enforce policy (pkg/policy.Enforcer)
6. If not dryRun AND decision.Action != "hold": execute scale (pkg/scaler.Executor)
7. Update status (lastDecisionReason, lastScaleTime, currentReplicas)
8. Record to observability (pkg/observability — stub for now, implement in Phase 6)
9. Return ctrl.Result{RequeueAfter: cooldownDuration}

Add all dependencies to the AgenticAutoscalerReconciler struct and inject them in
cmd/main.go using constructor injection (not global variables).

Run: make build
If compilation fails, show me the error and fix it.
```

### 5.4 — Smoke test on kind

```
Run the operator locally against the kind cluster and test the full reconcile loop:

1. make install    (installs CRDs into the kind cluster)
2. make run        (starts the operator locally with KUBECONFIG pointing to kind)

In a second terminal:
3. kubectl apply -f config/samples/payment-service-autoscaler.yaml
4. kubectl get agenticautoscaler -n production -w

Watch the operator logs. The reconciler should run and log a decision (hold, since
there is no real Prometheus in the kind cluster). Confirm:
- No panic
- Status is updated after first reconcile
- Requeue happens after cooldownSeconds

Then set AI_PROVIDER=rules before running to use the rule-based agent only.
Show me the controller log output.
```

**Phase 5 gate**: `make build` is clean. The operator reconciles on kind without panicking.
Status field `lastDecisionReason` is populated after the first reconcile.

---

## Phase 6 — Build observability

**Goal**: every `ScaleDecision` is recorded to an audit log and pushed to Grafana as an annotation.
The operator exposes its own Prometheus metrics.

---

### 6.1 — Build the decision recorder

```
Read .claude/skills/observability/SKILL.md.

Create pkg/observability/recorder.go with a Recorder interface:
  Record(ctx context.Context, aa AgenticAutoscaler, decision ScaleDecision) error

Create pkg/observability/configmap_recorder.go implementing Recorder:
- Appends a JSON DecisionRecord to a ConfigMap named "decision-audit-log"
  in the operator's namespace (env var: OPERATOR_NAMESPACE)
- Rotates: keeps only the last 200 entries
- DecisionRecord must include: timestamp, deployment, namespace, oldReplicas, newReplicas,
  action, reason, confidence, provider, patternsMatched, dryRun

Write a test using a fake k8s client from controller-runtime/pkg/client/fake.
Run: go test ./pkg/observability/...
```

### 6.2 — Build the Grafana annotation pusher

```
Create pkg/observability/grafana.go with GrafanaClient.PushAnnotation(ctx, DecisionRecord) error:

- POST to GRAFANA_URL/api/annotations
- Bearer token from GRAFANA_API_KEY env var (sourced from K8s Secret at startup)
- Annotation text format:
  "[scale-up] payment-service: 3 → 6 replicas. connection pool exhausted with latency spike"
- Tags: ["agentic-autoscaler", deployment-name, action]
- 5-second timeout
- If Grafana is unreachable: log a warning and return nil (never fail a reconcile over Grafana)

Write a test using httptest.NewServer.
Run: go test ./pkg/observability/...
```

### 6.3 — Add operator metrics

```
Create pkg/observability/metrics.go that registers these Prometheus metrics using
github.com/prometheus/client_golang/prometheus/promauto:

1. agentic_autoscaler_decisions_total (counter, labels: action, deployment, provider)
2. agentic_autoscaler_decision_latency_seconds (histogram, label: deployment)
3. agentic_autoscaler_ai_provider_errors_total (counter, label: provider)
4. agentic_autoscaler_cooldown_skips_total (counter, label: deployment)

Call the appropriate metric increments in:
- Reconcile() for decision latency and decisions_total
- The AI agent on error for ai_provider_errors_total
- The policy enforcer for cooldown_skips_total

Run: go test ./pkg/observability/...
Run: make build
```

### 6.4 — Plug observability into the controller

```
Update internal/controller/agentic_autoscaler_controller.go to:
1. Call recorder.Record() after every ScaleDecision (including hold and dry-run)
2. Call grafanaClient.PushAnnotation() only when action != "hold" AND dryRun == false
3. Increment the relevant metrics counter on every reconcile

Run: make build
Run: make run against kind — apply the sample CR and confirm the audit ConfigMap
is created in the operator namespace after the first reconcile.
```

**Phase 6 gate**: `make build` clean. After a reconcile, `kubectl get configmap decision-audit-log -n agentic-autoscaler-system -o yaml` shows at least one entry. Metrics endpoint (`curl localhost:8080/metrics | grep agentic_autoscaler`) returns the three counters.

---

## Phase 7 — Testing

**Goal**: full integration test suite using `envtest`. Load test scenario that proves
log-driven scaling fires before CPU-based HPA would.

---

### 7.1 — envtest integration tests for the controller

```
Read .claude/skills/operator-patterns/SKILL.md for the envtest setup pattern.

Create internal/controller/suite_test.go that sets up envtest with:
- The AgenticAutoscaler CRD loaded
- A fake Prometheus server (httptest) returning controllable metric values
- A fake Loki server (httptest) returning controllable log lines

Then create internal/controller/reconciler_integration_test.go with these test cases:

Test 1 — dry-run mode:
  Create an AgenticAutoscaler with dryRun: true
  Inject connection-pool-exhausted log + high latency
  Assert: Deployment replicas unchanged, status.lastDecisionReason contains "dry-run"

Test 2 — live scale-up from log pattern:
  Create an AgenticAutoscaler with dryRun: false, mode: calibrated
  Inject connection-pool-exhausted log + latency 2500ms + CPU 35%
  Assert: Deployment replica count increased, status updated, decision recorded

Test 3 — cooldown respected:
  Trigger a scale-up
  Immediately inject another scale-up signal
  Assert: second reconcile returns hold (cooldown active)

Test 4 — HPA bounds respected:
  Set spec.maxReplicas: 8, mock HPA with maxReplicas: 10
  Inject critical signal that would scale to 12
  Assert: target clamped to 8 (spec bound), not 10 or 12

Run: go test ./internal/controller/... -tags integration -v
```

### 7.2 — Unit test coverage check

```
Run the full test suite and report coverage:

go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out | grep -E "^total|pkg/policy|pkg/reasoning|pkg/fusion"

Target coverage:
- pkg/policy: > 85%
- pkg/reasoning: > 80%
- pkg/fusion: > 90%

For any package below target, identify the untested functions and add the missing tests.
```

### 7.3 — Simulate the "log fires before HPA" scenario

```
Write a test in internal/controller/proactive_test.go that demonstrates
the core value proposition of this project:

Setup:
- A Deployment starting at 3 replicas
- CPU at 45% (below HPA's 70% threshold — HPA would not fire)
- connection-pool-exhausted in logs + latency at 1800ms

Assert:
- The agentic autoscaler fires a scale-up decision
- The decision reason mentions the log pattern
- This happens before CPU would reach the HPA threshold

This test is the "money shot" for the conference demo. Make it clear and readable.
Print the timeline to stdout so it can be shown on screen.

Run: go test ./internal/controller/... -run TestProactivesScaling -v
```

**Phase 7 gate**: `go test ./... -tags integration` passes. `TestProactivesScaling` passes and its output clearly shows log-driven scaling firing at CPU 45% — before HPA's 70% threshold.

---

## Phase 8 — Helm chart and production rollout

**Goal**: the operator is installable in one `helm install` command. Production checklist passed.

---

### 8.1 — Scaffold the Helm chart

```
Create the Helm chart under deploy/helm/ with this structure:
- Chart.yaml (name: agentic-autoscaler, version: 0.1.0)
- values.yaml with all configuration exposed
- templates/deployment.yaml — operator Deployment
- templates/rbac.yaml — ServiceAccount, ClusterRole, ClusterRoleBinding
- templates/secret.yaml — placeholder for AI API key and Grafana key
- templates/networkpolicy.yaml — allows egress to AI API and ingress from Prometheus scrape
- charts/ollama/ — sub-chart (disabled by default, enabled with ollama.enabled=true)

The values.yaml must expose:
  image.repository, image.tag
  aiProvider.provider (default: "anthropic")
  aiProvider.secretRef
  hpa.coexistenceMode (default: "calibrated")
  dryRun (default: true)
  ollama.enabled (default: false)
  ollama.model (default: "llama3:8b")
  grafana.url, grafana.secretRef

Run: helm lint deploy/helm/
```

### 8.2 — Production secrets setup

```
Create the documentation file docs/production-setup.md that explains:

1. How to create the AI provider secret:
   kubectl create secret generic ai-provider-secret \
     --from-literal=ANTHROPIC_API_KEY=<key> \
     -n agentic-autoscaler-system

2. How to create the Grafana secret:
   kubectl create secret generic grafana-api-secret \
     --from-literal=GRAFANA_API_KEY=<key> \
     -n agentic-autoscaler-system

3. How to install the operator:
   helm install agentic-autoscaler deploy/helm/ \
     --namespace agentic-autoscaler-system \
     --create-namespace \
     --set aiProvider.provider=anthropic

4. How to opt in a deployment (points to config/samples/payment-service-autoscaler.yaml)

5. The dry-run → calibrated → owner rollout progression
```

### 8.3 — First production deployment (single service)

Use this prompt once you are ready to deploy to a real cluster:

```
I am ready to deploy the agentic autoscaler to production for the first time.
Target deployment: [YOUR_DEPLOYMENT_NAME]
Namespace: [YOUR_NAMESPACE]

Walk me through:
1. Confirming the cluster has Prometheus and Loki (or Kafka) running
2. Verifying the Prometheus scrape target for the deployment exists
3. Writing the AgenticAutoscaler CR for this deployment with:
   - dryRun: true
   - hpaCoexistence.mode: calibrated
   - conservative bounds (minReplicas slightly above HPA min, maxReplicas slightly below HPA max)
   - cooldownSeconds: 300
4. Applying it and watching the first reconcile
5. Querying the decision audit log after 24h to review what would have happened
```

### 8.4 — Promote from dry-run to live

Use this prompt after one week of dry-run observation:

```
I have been running the agentic autoscaler in dry-run mode for [N] days on [DEPLOYMENT].

Run /dry-run-report [DEPLOYMENT] [NAMESPACE] to summarize the decision history.

Then help me decide:
- Are the decisions sensible? Do the reasons match what I would have done manually?
- Were there any decisions with confidence < 0.5 that I should review?
- Are there any patterns firing too frequently (possible noise)?

If the analysis looks good, patch the AgenticAutoscaler to set dryRun: false.
Show me the kubectl patch command and confirm it applies cleanly.
```

**Phase 8 gate**: `helm lint` passes. Operator installs on the target cluster. First reconcile succeeds in dry-run mode. Decision audit log shows entries.

---

## Ongoing maintenance prompts

These prompts are for after the project is live.

---

### Add a new log pattern

```
/new-signal-pattern [pattern-name] "[regex]" [warning|critical]
```

Example:
```
/new-signal-pattern rate-limit-exceeded "rate limit exceeded|429 Too Many Requests|RATE_LIMITED" warning
```

---

### Add Ollama self-hosted support

```
/scaffold-provider ollama
```

Then follow up with:
```
After scaffolding the Ollama provider, also:
1. Update deploy/helm/charts/ollama/values.yaml with the node selector and taint tolerations
2. Update docs/production-setup.md with Ollama node requirements (12GB RAM minimum)
3. Write a test that confirms OllamaAgent falls back to RuleBasedAgent on a 30s timeout
```

---

### Review a PR before merging

```
/review
```

Or from GitHub Actions automatically on every PR open (configured in `.github/workflows/pr-claude-code-review.yml`).

---

### Investigate a suspicious scale decision

```
The agentic autoscaler scaled [DEPLOYMENT] from [N] to [M] replicas at [TIME].
I want to understand why.

1. Read the decision audit log entry for that timestamp
2. Show me what log patterns were matched
3. Show me the metric values that were collected at that time
4. Was the AI agent or the rule-based agent used?
5. Do you think this decision was correct? If not, what rule or prompt change would prevent it?
```

---

### Switch AI provider without redeployment

```
I want to switch the agentic autoscaler from Anthropic to Ollama for the [DEPLOYMENT] deployment.

1. Confirm the Ollama deployment is running: kubectl get deploy -n ollama
2. Update the AI provider secret to set AI_PROVIDER=ollama and OLLAMA_BASE_URL
3. Patch the operator Deployment to pick up the new env vars (rolling restart)
4. Watch the next reconcile and confirm the decision log shows provider: "ollama"
```

---

## Quick reference — all slash commands

| Command | When to use |
|---------|-------------|
| `/onboard [task description]` | Before starting any new feature or fix |
| `/new-signal-pattern [name] [regex] [severity]` | Add a new log pattern to detect |
| `/scaffold-provider [name]` | Add a new AI provider (ollama, openai, etc.) |
| `/dry-run-report [deployment] [namespace]` | Summarize dry-run decisions before going live |
| `/review` | Run the code-reviewer agent on current changes |

---

## Troubleshooting prompts

### Reconciler is not firing

```
The AgenticAutoscaler controller does not seem to be reconciling my CR.
1. Check: kubectl get agenticautoscaler -A
2. Check: kubectl describe agenticautoscaler [NAME] -n [NS]
3. Check controller logs: kubectl logs -n agentic-autoscaler-system deploy/agentic-autoscaler-controller-manager
4. Is the CRD installed? kubectl get crd | grep agenticautoscaler
5. Does the ServiceAccount have the right RBAC? kubectl auth can-i get deployments --as=system:serviceaccount:agentic-autoscaler-system:agentic-autoscaler-controller-manager
Diagnose and fix.
```

### AI provider is returning errors

```
The AI provider is failing and all decisions are falling back to rule-based.
Check: kubectl logs -n agentic-autoscaler-system deploy/agentic-autoscaler-controller-manager | grep "ai_provider"
1. Is the secret mounted correctly? kubectl describe pod -n agentic-autoscaler-system [POD]
2. Is the API key valid? Test with a curl to the provider endpoint
3. Is it a timeout? Is the 10s timeout too short for this model?
Diagnose and recommend a fix.
```

### HPA and agentic autoscaler are fighting

```
I see the replica count oscillating: the agentic autoscaler scales up, then HPA scales down.
This means they are conflicting.

1. Read .claude/rules/hpa-coexistence.md
2. Check: kubectl get hpa [HPA_NAME] -n [NS] -o yaml — what are its minReplicas and maxReplicas?
3. Check: kubectl get agenticautoscaler [NAME] -n [NS] -o yaml — are the bounds nested inside HPA?
4. If not, propose the corrected bounds and the kubectl patch command to fix it
5. Should we switch to mode: owner now, or keep calibrated?
```
