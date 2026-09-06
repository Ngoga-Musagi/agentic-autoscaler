# agentic-autoscaler

Kubernetes-native operator that extends autoscaling beyond CPU/memory by fusing
Prometheus metrics with log-stream intelligence and AI reasoning to make
proactive, explainable scaling decisions.

## Description

`agentic-autoscaler` watches `AgenticAutoscaler` custom resources, each of which
opts a single `Deployment` into agentic autoscaling. On every reconcile it:

1. **Collects signals** — Prometheus metrics (latency, error rate, CPU, RPS) and
   log streams from Loki (`pkg/signals`).
2. **Fuses them** — correlates metrics with log patterns such as
   `connection-pool-exhausted` or `oom-killed` into a `FusedSignal` (`pkg/fusion`).
3. **Reasons** — a rule-based agent (offline) or a cloud / self-hosted LLM
   (Anthropic, OpenAI, Ollama) produces an explainable `ScaleDecision`
   (`pkg/reasoning`).
4. **Enforces policy** — clamps to `min/max` bounds, honours cooldown, and stays
   within HPA bounds in calibrated coexistence mode (`pkg/policy`).
5. **Acts** — patches the Deployment's replicas (or a KEDA `ScaledObject`),
   unless `dryRun` is set (`pkg/scaler`).
6. **Explains** — records every decision to an audit ConfigMap and pushes a
   Grafana annotation (`pkg/observability`).

A `Deployment` is **only** managed when a matching `AgenticAutoscaler` CR targets
it — there is no implicit or global behavior.

## Project status & maturity

This is a **vendor-neutral reference implementation** — reproducible on a laptop
with Docker + `kind`, no cloud account required. It is not a supported product.
Each capability below is tagged with its honest maturity so nothing here is
overstated:

- **Implemented** — code exists on the reconcile path.
- **Tested** — covered by unit and/or envtest integration tests.
- **Observed** — seen working end-to-end in a live `kind` run.
- **Roadmap** — designed but not yet built, or built but not yet reachable.

| Capability | Maturity |
|------------|----------|
| Prometheus + Loki signal collection → `SystemSnapshot` | Implemented · Tested |
| Typed signal fusion + log-pattern matching → `FusedSignal` | Implemented · Tested |
| Rule-based reasoning agent (offline, deterministic) | Implemented · Tested |
| Cloud LLM providers (Anthropic, OpenAI) | Implemented · Tested |
| Self-hosted Ollama provider | Implemented · Tested |
| Fail-safe degradation (AI error → rules → `hold`) | Implemented · Tested |
| Policy: min/max bounds + cooldown enforcement | Implemented · Tested |
| HPA calibrated coexistence (nested bounds, read-only HPA) | Implemented · Tested |
| Deployment scaler (merge-patch `spec.replicas` only) | Implemented · Tested |
| Decision audit log (ConfigMap) | Implemented · Tested |
| Grafana decision annotations | Implemented · Tested · Observed |
| Natural-language decision explanations | Implemented · Tested |
| Web console (explore / onboard / manage / load) | Implemented · Observed |
| KEDA `ScaledObject` executor | Implemented · Tested — not yet selectable via `spec` (Roadmap) |
| Detection across *N consecutive log windows* | Implemented · Tested |
| Custom log patterns via the CR spec | Implemented · Tested |
| Sustained-quiet scale-down (rule 5) across reconciles | Implemented · Tested |
| Log-pattern-driven end-to-end demo (timeout storm) | Implemented · Tested · Observed |
| Quantitative A/B evidence (detection & scaling lead-time, restraint) | Roadmap |
| PR CI (build/test/lint/smoke) + multi-arch release workflow | Workflows added — first green run pending a PR/tag |

## Getting Started

**The one command:** on a machine with Docker + Helm, run

```sh
bash hack/bootstrap.sh
```

That is the single canonical entrypoint — it creates a local `kind` cluster and
installs the whole stack (Prometheus, Loki, Grafana, the operator, a sample
workload, and an `AgenticAutoscaler` CR). Everything else on this page is an
**alternative or advanced** path: [Option B](#option-b--run-locally-without-a-container-advanced)
runs the operator as a bare Go process, [Option C](#option-c--install-with-helm-advanced)
installs via Helm, and `make dev-setup` is a thin wrapper around the same
bootstrap. New here? Use the one command above.

> [!WARNING]
> This operator **patches `Deployment` replica counts**. Always start with
> `dryRun: true` (the default) so decisions are logged but not applied.
> A Deployment is only ever managed when an `AgenticAutoscaler` CR targets it —
> there is no implicit or global behavior.

> [!NOTE]
> Calibrated HPA coexistence is the **recommended** mode for initial rollout, but
> it is not a default — the CRD has no default for `spec.hpaCoexistence.mode`, so
> set it explicitly (`owner` or `calibrated`) on each CR.

### Option A — Full stack on kind (recommended)

One script creates a local `kind` cluster and installs everything: Prometheus,
Loki, Grafana, the operator, a sample workload, and an `AgenticAutoscaler` CR.
All three UIs come up on localhost via a second script.

**Prerequisites:** [Docker Desktop](https://www.docker.com/products/docker-desktop/)
running + [Helm v3](https://helm.sh/docs/intro/install/).
`kind` and `kubectl` are downloaded automatically if missing.

```sh
# Terminal 1 — bootstrap (≈ 10-15 min on first run, mostly image pulls)
bash hack/bootstrap.sh

# Optional: enable AI reasoning (falls back to rule-based agent if unset)
ANTHROPIC_API_KEY=sk-ant-... bash hack/bootstrap.sh
```

When bootstrap prints **"Bootstrap complete!"**, open a second terminal:

```sh
# Terminal 2 — keep this open; Ctrl-C stops all forwards
bash hack/port-forward.sh
```

#### Accessing the UIs

| UI | URL | Credentials | Purpose |
|----|-----|-------------|---------|
| **Grafana** | http://localhost:3000 | `admin` / `admin` | Decision timeline, metric panels, audit table |
| **Console** | http://localhost:8090 | — | Ask questions, onboard deployments, manage autoscalers, drive load |
| **Prometheus** | http://localhost:9090 | — | Raw metrics, PromQL explorer |

##### Grafana — the SRE "why" view

Navigate to **Dashboards → Agentic Autoscaler – Scaling Decisions**.

- Stat panels at the top show total decisions, scale-ups, scale-downs, and AI
  errors in the last 24 hours.
- The **Decision Timeline** time-series shows when each action fired.
- The **Audit Log** table (bottom) shows every decision with its reason, replica
  change, confidence score, and matched patterns (e.g. `connection-pool-exhausted`).
- **Red annotation lines** overlay all panels: hover one to read the full
  AI-generated explanation — *when* it scaled, *from → to*, and *why*.

Set the time range (top-right) to **Last 1 hour** to see recent decisions.

##### Console — explore, onboard, manage, load-test

Open http://localhost:8090. The console has four tabs:

- **Explore** — ask any question about scaling decisions, e.g. *"Why did
  payment-service scale up at 14:32?"* The right panel shows a live decision
  timeline (refreshes every 30s); the deployment filter focuses on one service.
  If `ANTHROPIC_API_KEY` is set answers come from Claude, otherwise a structured
  plain-text summary.
- **Autoscalers** — every Deployment under agentic control, with its bounds,
  current replicas, dry-run state, and last decision. In write mode you can flip
  dry-run/live, edit bounds, or stop managing a deployment.
- **Onboard** — a form that creates an `AgenticAutoscaler` for any Deployment in
  any namespace: pick the target, point at your Prometheus/Loki, set bounds and
  provider, and apply — no kubectl or YAML. Already-managed deployments are
  greyed out.
- **Load** — drive the load generator and watch replicas react (shown only when
  a load generator is configured — see below).

**Read-only by default.** The Autoscalers/Onboard/Load actions that *change* the
cluster are disabled until you enable write mode. The header badge shows
`read-only` or `read-write`. To enable it (and require a bearer token), install
the chart with `console.writeEnabled=true` (and optionally
`console.authSecretRef`). See [.claude/rules/security.md](.claude/rules/security.md).

#### Watch it autoscale under real load

This drives **real HTTP traffic** at the sample service so the operator's
Prometheus signals climb and it scales up — then stops, and you watch it scale
back down. (The offline `hack/mockserver` demo can't do this; it returns a fixed
"overloaded" snapshot.)

```sh
# 1. Build + deploy the load generator, and point the console at it.
make demo-load          # builds the loadgen image, deploys it, enables the Load tab

# 2a. Drive load from the browser: open http://localhost:8090 → Load tab,
#     set RPS, press Start, then open the Autoscalers tab to watch replicas climb.

# 2b. …or from the terminal:
make load-up RPS=300                 # sustained 300 rps at payment-service
kubectl -n production get deploy payment-service -w   # watch replicas climb
make load-down                       # stop — replicas scale back down after cooldown

# One-shot up→hold→down cycle:
RPS=300 HOLD=240 bash hack/loadtest.sh
```

To exercise the deterministic rules, shape the traffic:
`make load-up RPS=300 ERRPCT=15` (drives error rate > 10%) or
`make load-up RPS=300 DELAYMS=2500` (drives p99 latency > 2s).

The load generator can target **any** service, not just the sample —
`make load-up LOAD_TARGET=http://my-svc.my-ns:8080`. It is a demo/testing tool
and is never installed by the default bootstrap.

#### Watch live decisions in the terminal

```sh
# Stream operator logs (JSON structured, one line per reconcile)
kubectl -n agentic-autoscaler-system logs \
  -l app.kubernetes.io/name=agentic-autoscaler -f

# Watch the CR status update after each decision
kubectl get agenticautoscaler -n production -w

# Read the full audit log (raw ConfigMap)
kubectl get configmap decision-audit-log \
  -n agentic-autoscaler-system -o jsonpath='{.data.decisions}' | python3 -m json.tool
```

#### Tear down

```sh
kind delete cluster --name agentic-autoscaler-dev
```

---

### Option B — Run locally without a container (advanced)

The operator runs as a plain Go process on your machine, talking to the cluster
through your kubeconfig. No image build required.

```sh
# 1. Point kubectl at a local cluster
kubectl config use-context docker-desktop      # or: minikube / kind-...

# 2. Install the CRD
kubectl apply -f config/crd/bases/scaling.autoscaler.io_agenticautoscalers.yaml

# 3. Run with the offline rule-based agent (no API key needed)
export AI_PROVIDER=rules
export OPERATOR_NAMESPACE=default
go run ./cmd/main.go
```

In a second terminal, opt a Deployment in (the sample defaults to `dryRun: true`):

```sh
kubectl apply -f config/samples/payment-service-autoscaler.yaml
kubectl get agenticautoscaler -A -w
```

The reconcile loop fires and logs a decision. Without a reachable Prometheus/Loki
it logs signal errors and decides `hold` — that is expected, and it proves the
loop runs.

> On **Windows PowerShell**, set environment variables with
> `$env:AI_PROVIDER = "rules"` instead of `export`.

To use a cloud LLM:

```sh
export AI_PROVIDER=anthropic           # or: openai
export ANTHROPIC_API_KEY=sk-ant-...    # or: OPENAI_API_KEY=sk-...
go run ./cmd/main.go
```

| Variable | Used when | Purpose |
|----------|-----------|---------|
| `AI_PROVIDER` | always | `rules` (default) \| `anthropic` \| `openai` \| `ollama` |
| `ANTHROPIC_API_KEY` / `ANTHROPIC_MODEL` | provider=anthropic | API key + optional model override |
| `OPENAI_API_KEY` / `OPENAI_MODEL` | provider=openai | API key + optional model override |
| `OLLAMA_BASE_URL` / `OLLAMA_MODEL` | provider=ollama | Ollama endpoint + model |
| `OPERATOR_NAMESPACE` | always | Namespace for the decision-audit ConfigMap |
| `GRAFANA_URL` / `GRAFANA_API_KEY` | optional | Push decision annotations to Grafana |

#### Full local demo (AI reasoning + Grafana annotations)

```powershell
# Windows PowerShell — on macOS/Linux replace $env: with export and .exe with nothing

# 1. Start a local cluster
minikube start --driver=docker

# 2. Build operator + mock signal server
go build -o bin/manager.exe ./cmd/main.go
go build -o bin/mockserver.exe ./hack/mockserver

# 3. Install CRD and create sample workload
kubectl apply -f config/crd/bases/scaling.autoscaler.io_agenticautoscalers.yaml
kubectl create deployment payment-service --image=nginx:alpine --replicas=3

# 4. Set your API key
Copy-Item .env.example .env
# edit .env: AI_PROVIDER=anthropic  ANTHROPIC_API_KEY=sk-ant-...  ANTHROPIC_MODEL=claude-sonnet-4-6

# 5. Start Grafana + pre-built dashboard
powershell -ExecutionPolicy Bypass -File hack\grafana-demo.ps1

# 6. Start mock signal server (simulates connection-pool overload on :9099)
Start-Process bin\mockserver.exe

# 7. Run the operator
powershell -ExecutionPolicy Bypass -File hack\run-operator.ps1 -Background

# 8. Opt the workload in
kubectl apply -f hack/local-demo-autoscaler.yaml

# 9. Watch replicas climb
kubectl get deploy payment-service -w
```

> [!NOTE]
> The hardcoded default model in `pkg/reasoning/anthropic.go` may be retired.
> Set `ANTHROPIC_MODEL=claude-sonnet-4-6` (or another current model ID) to be safe.

```powershell
# Tear down
Get-Process manager,mockserver -ErrorAction SilentlyContinue | Stop-Process -Force
docker rm -f grafana
kubectl delete -f hack/local-demo-autoscaler.yaml
minikube delete
```

---

### Option C — Install with Helm (advanced)

See [`docs/production-setup.md`](docs/production-setup.md) for the full rollout
guide (dry-run → calibrated → owner progression).

```sh
# Build and push image
make docker-build IMG=ghcr.io/<your-org>/agentic-autoscaler:0.1.0
make docker-push  IMG=ghcr.io/<your-org>/agentic-autoscaler:0.1.0

# Lint and install
helm lint deploy/helm/
helm install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  --set image.repository=ghcr.io/<your-org>/agentic-autoscaler \
  --set image.tag=0.1.0 \
  --set aiProvider.provider=anthropic \
  --set aiProvider.secretRef=<your-secret-name> \
  --set dryRun=true
```

Common chart values (full list in `deploy/helm/values.yaml`):

| Value | Default | Purpose |
|-------|---------|---------|
| `aiProvider.provider` | `anthropic` | `anthropic` \| `openai` \| `ollama` |
| `aiProvider.secretRef` | `agentic-autoscaler-ai-key` | Secret holding `API_KEY` |
| `hpa.coexistenceMode` | `calibrated` | `calibrated` (nested in HPA bounds) or `owner` |
| `dryRun` | `true` | Log decisions without applying them |
| `ollama.enabled` | `false` | Deploy the bundled Ollama sub-chart |
| `ollama.model` | `llama3:8b` | Self-hosted model to serve |
| `grafana.url` | `""` | Grafana base URL for decision annotations |

### Deploy on the cluster (Kustomize alternative)
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/agentic-autoscaler:tag
```

**NOTE:** This image ought to be published in the personal registry you specified. 
And it is required to have access to pull the image from the working environment. 
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/agentic-autoscaler:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin 
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Autoscaling Demo

End-to-end walkthrough of driving real traffic at the sample service and watching
the operator scale it **up** (load climbs) and back **down** (load stops). Assumes
the full stack from [Option A](#option-a--full-stack-on-kind-recommended) is
running.

### 1. Bring the demo up with the Load tab enabled

The console's **Load** tab and write actions are **off by default** (security:
write mode is opt-in). The bootstrap flag turns them on and deploys the load
generator in one shot:

```sh
WITH_LOADGEN=1 bash hack/bootstrap.sh
```

> [!IMPORTANT]
> A later **plain** `bash hack/bootstrap.sh` resets the console flags back to the
> chart defaults (write off, no load generator), which **hides the Load tab again**
> even though the load-generator workload may still be running. Always re-run with
> `WITH_LOADGEN=1` to keep the demo wired up.

### 2. Enable the Load tab on an already-running cluster

If the stack is already up and the Load tab is missing, flip the two console flags
without rebuilding the image (`--reuse-values` preserves your AI key and all other
settings):

```sh
helm upgrade agentic-autoscaler deploy/helm -n agentic-autoscaler-system --reuse-values \
  --set console.writeEnabled=true \
  --set console.loadgenURL=http://loadgen.loadgen.svc.cluster.local:8080

kubectl -n agentic-autoscaler-system rollout status \
  deploy/agentic-autoscaler-agentic-autoscaler
```

Verify the served config — the Load tab appears only when `loadgenEnabled` is true:

```sh
kubectl -n agentic-autoscaler-system run cfgcheck --rm -i --restart=Never \
  --image=curlimages/curl:8.7.1 --command -- \
  curl -s http://agentic-autoscaler-agentic-autoscaler:8090/api/config
# → {"writeEnabled":true,"authRequired":false,"loadgenEnabled":true}
```

### 3. Open the console and switch to live scaling

```sh
bash hack/port-forward.sh        # Terminal 2 — keep open
```

Open http://localhost:8090 and **hard-refresh (Ctrl-Shift-R)** to clear cached JS —
the **Load** tab should now be visible.

By default the sample CR runs `dryRun: true`, so decisions are logged but replicas
**do not change**. To see real scaling, flip it to live:

```sh
kubectl -n production patch agenticautoscaler payment-service-autoscaler \
  --type merge -p '{"spec":{"dryRun":false}}'
```

> [!WARNING]
> `dryRun: false` lets the operator **patch replica counts**. The sample CR is in
> `calibrated` mode (nested within HPA bounds, min 3 / max 18). If a matching HPA
> exists it acts as a safety net; see
> [.claude/rules/hpa-coexistence.md](.claude/rules/hpa-coexistence.md).

### 4. Drive load and watch it react

From the **Load** tab: set an RPS, press **Start**, then open the **Autoscalers**
tab to watch replicas climb; press **Stop** to watch them fall after cooldown.

Or from the terminal:

```sh
make load-up RPS=300                                  # sustained 300 rps
kubectl -n production get deploy payment-service -w   # watch replicas climb
make load-down                                        # stop; replicas fall after cooldown

# Shape traffic to trigger the deterministic rules:
make load-up RPS=300 ERRPCT=15                        # error rate > 10%
make load-up RPS=300 DELAYMS=2500                     # p99 latency > 2s

# One-shot up→hold→down cycle:
RPS=300 HOLD=240 bash hack/loadtest.sh
```

### Troubleshooting: the Load tab is missing

| Check | Command | Expected |
|-------|---------|----------|
| Console config | the `curl …/api/config` in step 2 | `loadgenEnabled:true` |
| Operator env | `kubectl -n agentic-autoscaler-system get deploy -o jsonpath='{range .items[*].spec.template.spec.containers[*].env[*]}{.name}={.value}{"\n"}{end}'` | `CONSOLE_WRITE_ENABLED=true`, `LOADGEN_URL=…` |
| Load generator | `kubectl -n loadgen get deploy,svc` | `loadgen` Deployment + Service present |

If `loadgenEnabled` is false, the flags were reset — re-apply step 2. If the env
vars are correct but the tab is still hidden, the browser cached the old console
JS: **hard-refresh (Ctrl-Shift-R)**.

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/agentic-autoscaler:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/agentic-autoscaler/<tag or branch>/dist/install.yaml
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

