# Multi-service demo — three services, one operator, one dashboard

DEMO / TESTING ONLY. Shows the same unchanged operator handling three very
different services, each configured only by its `AgenticAutoscaler` CR:

| Service | Namespace | What it proves | CR |
|---|---|---|---|
| `checkout` | `shop` | a service with **its own metric names** (`shop_request_seconds`, `shop_requests_total{app,code}`) is onboarded with `spec.metrics` alone | `config/samples/dev-checkout-autoscaler.yaml` |
| `payment-service` | `production` | a standard HTTP API next to an existing HPA (calibrated mode); errors that are not load-related | `config/samples/payment-service-autoscaler.yaml` |
| `agent-runner` | `agents` | AI-agent workers: scale on **backlog**, **hold** when the LLM provider rate-limits, cost guardrail on the token budget | `config/samples/dev-agent-runner-autoscaler.yaml` |

## Bring it up

```bash
make demo            # = WITH_LOADGEN=1 bash hack/bootstrap.sh && bash hack/demo.sh multi-setup
# without make (Git Bash on Windows):
WITH_LOADGEN=1 bash hack/bootstrap.sh
bash hack/demo.sh multi-setup
```

Open Grafana → **Agentic Autoscaler — Multi-service demo** (replicas, p99,
CPU, agent backlog / 429 % / token budget, decisions and guardrails, and the
decision log), and in a second terminal:

```bash
bash hack/demo.sh multi-watch
```

The load generator drives one target at a time, so run the scenarios one after
another. `bash hack/demo.sh reset-all` returns everything to baseline (about
3–5 minutes) — run it a few minutes before presenting.

## Scenarios

### 1 · checkout — timeout storm, CPU blind
```bash
bash hack/demo.sh checkout-storm-up      # … checkout-storm-down
```
checkout answers in > 2 s and logs `connection pool exhausted`, while its CPU
stays near idle. The operator reads p99 through the CR's `shop_*` query and the
pattern through Loki, and rule 2 (or the LLM) scales it. A CPU HPA sees nothing.

### 2 · payment-service — restraint
```bash
bash hack/demo.sh errors-up              # … errors-down
```
25 % errors at low load. With an LLM provider configured the agent **holds**:
the errors are not load-related, so more pods would not help. Be honest on
stage about the fallback: on the rule engine alone, rule 3 scales on > 10 %
errors.

### 3 · agent-runner — backlog: scale
```bash
bash hack/demo.sh agent-backlog-up       # … agent-down
```
40 tasks/s against 2 runners that carry 16/s. Every task waits on the LLM and a
tool, so CPU stays flat while the queue grows. The backlog rule asks for
`ceil(queued / 2)` runners (within min/max) — about half a runner's four
worker slots queued, beyond which tasks start to wait.

### 4 · agent-runner — provider rate limit: hold
```bash
bash hack/demo.sh agent-429-up           # … agent-down
```
The mock LLM's quota — shared by every runner, like an API key's — is squeezed
to 4 calls/s. Tasks fail and queue and the error rate climbs, which would make
a queue-length or error-rate autoscaler add runners. The operator **holds**,
with the reason on the dashboard:

> held: the LLM provider is rate-limiting N% of calls (hold at 20%) — its quota
> is shared by every replica, so more workers would only add calls against it

### 5 · agent-runner — token budget: cost guardrail
```bash
bash hack/demo.sh agent-budget-up        # … agent-down
```
The same task surge, but the provider's token budget is ~110 % used. The queue
grows exactly as in scenario 3, and every scale-up is held by the cost
guardrail, with the scale-up it blocked in the reason.

### Optional · advisor mode with KEDA
```bash
bash hack/demo.sh advisor-on             # faultgen scaled by KEDA on max(CPU, agent)
```
See [integrating-existing-infra.md](integrating-existing-infra.md#hpa-coexistence).

## What was observed on kind (2026-11-22, rule engine)

One run each, on the dev `kind` cluster, with the reasoning on the rule engine
(the LLM key was not configured). n=1 — a demonstration, not a benchmark.

| Scenario | Observed |
|---|---|
| checkout storm | p99 read from `shop_request_seconds` via the CR; scaled **2 → 3 → 5 → 8 → 10** in ~3.5 min, **10 → 3** after the 2-min quiet window. Re-run 2026-11-24: pinned at the max, each decision is a `replica-bounds` hold ("held: already at 10 replicas, the most this policy allows") instead of "scale-up 10 → 10" |
| agent backlog, config only (no `spec.backlog`/`spec.agentic`) | the unchanged rules scaled **2 → 3 → 5** on `worker pool saturated` + task p99 > 2 s, then **5 → 3** |
| agent 429 storm (quota squeezed to 4 calls/s) | 77–83 % of LLM calls rejected; **held for 6 min** at 3 runners, each decision recording `guardrail: llm-rate-limit` and the scale-up it blocked (to 5) |
| agent token budget exhausted | **held for 4 min**, `guardrail: token-budget`, reason naming the blocked scale-up (to 4–6) |
| agent backlog, budget restored | backlog rule scaled **3 → 4 → 6** while runner CPU stayed at **0.4–0.8 %** — a CPU HPA would not have moved |
| `reset-all` | all three services back to baseline (agent-runner 6 → 3) |

The runs also surfaced (and the branch fixes) three existing issues: a service
pinned at `maxReplicas` logged "scale-up 10 → 10" every cycle (now an explained
`replica-bounds` hold, confirmed live), a cooldown at the max said "would have
scaled to 10 replicas" from 10 (now reported as the bound), and the scale-down
reason always said "15+ minutes" even with a 2-minute window (now it states the
configured window).

## What was observed on kind (2026-11-25, Claude reasoning)

Same scenarios, same CRs, with `AI_PROVIDER=anthropic` (`claude-sonnet-4-6`).
91 decisions came from the model, none fell back to the rules. n=1 — a
demonstration, not a benchmark. Reasons are quoted from the decision log.

| Scenario | Observed |
|---|---|
| checkout storm | scaled **2 → 5 → 9 → 10** in ~2.3 min (the rules took ~6 min in 1-2-3 steps). At the max it held and said *why more pods would not help*: "CPU is only 0.1 % … suggesting the bottleneck is an external dependency or connection pool config". **10 → 2** in one step after the storm |
| payment-service errors (restraint) | **held at 3 for 5 min** at 24–26 % errors: "errors are likely application-level (e.g., bad requests or upstream dependency failures) rather than capacity-related". The rule engine scales here (rule 3) |
| agent 429 storm | **held at 2 for 4 min** with 74–75 % of LLM calls rejected: "additional replicas would not relieve the bottleneck". The model held on its own, so the deterministic `llm-rate-limit` guardrail (the backstop) had nothing to override |
| agent backlog surge | scaled **2 → 6 → 9 → 11**, then — **with the surge still running** — **11 → 2** ("near-zero queue depth … scaling down to 2 replicas is sufficient for 38 rps"), and back up **2 → 6 → 9**. See below |
| `reset-all` | all services back to baseline |

**Known issue — scale-down flapping on the backlog.** Once 11 runners had
drained the queue, the model read the empty queue as over-provisioning and
dropped to the minimum, although 2 runners carry ~16 tasks/s of the 40/s
arriving; the queue refilled and it scaled up again. The model sees queue
depth but not per-replica capacity, and nothing limited the size of a
scale-down. Until this is fixed, set `spec.policy.maxScaleStep` on backlog
workloads (it caps both directions) and present the backlog scenario on the
rule engine, where rule 5 does not scale down while the backlog needs capacity.
