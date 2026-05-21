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

## Getting Started

### Prerequisites
- go version v1.21.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.
- _(optional)_ helm v3.x — for the Helm install path below.

For a quick local trial, a local cluster is fine: Docker Desktop's built-in
Kubernetes, `minikube`, or `kind`.

> [!WARNING]
> This operator **patches `Deployment` replica counts**. Always start with
> `dryRun: true` (the sample default) so decisions are only logged, and confirm
> your `kubectl` context targets the cluster you intend — not production. A
> Deployment is only ever managed when an `AgenticAutoscaler` CR targets it.

### Run locally (development)

The operator runs as a process on your machine, talking to the cluster through
your kubeconfig. No image build required — the lightest way to see it work.

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

To use a cloud LLM instead of the rule-based agent, set the provider and its key
(read directly from the environment by `pkg/reasoning`):

```sh
export AI_PROVIDER=anthropic           # or: openai
export ANTHROPIC_API_KEY=sk-ant-...    # or: OPENAI_API_KEY=sk-...
go run ./cmd/main.go
```

| Variable | Used when | Purpose |
|----------|-----------|---------|
| `AI_PROVIDER` | always | `rules` (default, offline) \| `anthropic` \| `openai` \| `ollama` |
| `ANTHROPIC_API_KEY` / `ANTHROPIC_MODEL` | provider=anthropic | API key + optional model override |
| `OPENAI_API_KEY` / `OPENAI_MODEL` | provider=openai | API key + optional model override |
| `OLLAMA_BASE_URL` / `OLLAMA_MODEL` | provider=ollama | Ollama endpoint + model |
| `OPERATOR_NAMESPACE` | always | Namespace for the decision-audit ConfigMap |
| `GRAFANA_URL` / `GRAFANA_API_KEY` | optional | Push decision annotations to Grafana |

### Full local walkthrough — AI reasoning + Grafana dashboard

This runs the **entire system** on a local cluster end to end: a sample
workload, a mock Prometheus/Loki server that simulates an incident, **Claude**
making the scaling decisions, and a **Grafana dashboard** where an SRE sees
*why* each scale-up/down happened.

Commands are PowerShell (Windows — the environment this was built on). On
macOS/Linux, replace `$env:X="y"` with `export X=y` and `bin\*.exe` with `bin/*`,
and run the `.ps1` helpers with `pwsh`.

**Prerequisites:** Go 1.21+, Docker running, `minikube`, `kubectl`, and an
Anthropic API key.

```powershell
# 1. Start a local cluster
minikube start --driver=docker

# 2. Build the operator and the mock signal server
go build -o bin/manager.exe ./cmd/main.go
go build -o bin/mockserver.exe ./hack/mockserver

# 3. Install the CRD and create a sample workload (3 replicas)
kubectl apply -f config/crd/bases/scaling.autoscaler.io_agenticautoscalers.yaml
kubectl create deployment payment-service --image=nginx:alpine --replicas=3

# 4. Configure secrets: copy the template and fill in your key
Copy-Item .env.example .env
#   edit .env →  AI_PROVIDER=anthropic
#                ANTHROPIC_API_KEY=sk-ant-...
#                ANTHROPIC_MODEL=claude-sonnet-4-6     (a CURRENT model id — see note)

# 5. Stand up Grafana + a pre-built dashboard, and point .env at it
#    (run this BEFORE the operator so GRAFANA_URL is set when it launches)
powershell -ExecutionPolicy Bypass -File hack\grafana-demo.ps1   # prints the dashboard URL

# 6. Start the mock Prometheus+Loki signal server (overload scenario) on :9099
Start-Process bin\mockserver.exe

# 7. Run the operator — the loader reads .env (provider, key, GRAFANA_URL)
powershell -ExecutionPolicy Bypass -File hack\run-operator.ps1 -Background

# 8. Opt the workload in (live scaling, mock signals, owner mode)
kubectl apply -f hack/local-demo-autoscaler.yaml

# 9. Watch the operator scale it up
kubectl get deploy payment-service -w
```

Within a few seconds the operator collects the mock overload signals, Claude
decides to scale up, and replicas climb from 3 toward `maxReplicas` (20) — each
step recorded in the audit log and annotated on the Grafana dashboard.

> [!NOTE]
> The hardcoded default model in `pkg/reasoning/anthropic.go` may be retired; if
> the API rejects it the operator **silently falls back to the rule-based agent**.
> Setting `ANTHROPIC_MODEL` to a current id (e.g. `claude-sonnet-4-6`) avoids that.
> The demo CR uses `dryRun: false` and `mode: owner` for immediacy — in production
> start with `dryRun: true`.

#### Using the Grafana dashboard (the SRE "why" view)

`hack/grafana-demo.ps1` prints a URL like:

```
http://localhost:3000/d/agentic-decisions/agentic-autoscaler-scaling-decisions
```

Open it in a browser **on this machine** — anonymous viewing is enabled, no login
(if ever prompted, `admin` / `admin`). Set the time range (top-right) to **Last 1 hour**.

- Each **red vertical line** is a scale decision at the exact moment it fired.
- **Hover a line** to read Claude's reason and the replica change, e.g.
  *"[scale-up] payment-service: 3 → 6 replicas. Critical connection pool exhaustion
  with p99 latency at 2500ms and 3% error rate … warranting doubling replicas."*
- Lines are tagged `agentic-autoscaler`, the deployment name, and the action, so
  you can filter the annotation layer by service or by scale-up/scale-down.

This is the explainability surface for on-call SREs: *when* it scaled, *from → to*,
and *why* — without reading operator logs.

**How it works:** for every real (non-hold, non-dry-run) scale action the operator
POSTs to `GRAFANA_URL/api/annotations`. Annotations are global (no dashboard UID),
so they also overlay your existing service dashboards — the latency spike and the
autoscaler's "why" line up on the same timeline. Scope to one dashboard by setting
`GRAFANA_DASHBOARD_UID`.

The same reasoning is available without Grafana too:

| Surface | How to view |
|---|---|
| Latest decision | `kubectl get agenticautoscaler payment-service-autoscaler -o jsonpath='{.status.lastDecisionReason}'` |
| Full audit history | `kubectl get configmap decision-audit-log -n default -o yaml` |
| Operator metrics | open `http://localhost:8080/metrics`, search `agentic_autoscaler_` |

#### Tear down

```powershell
Get-Process manager,mockserver -ErrorAction SilentlyContinue | Stop-Process -Force
docker rm -f grafana
kubectl delete -f hack/local-demo-autoscaler.yaml
minikube delete            # optional: removes the whole local cluster
```

### Install with Helm

Ships the operator as a container into the cluster. Requires a running Docker
daemon to build the image.

```sh
# Build (and push, or load into kind/minikube) the operator image
make docker-build IMG=ghcr.io/<your-org>/agentic-autoscaler:0.1.0

# Lint, then install the chart
helm lint deploy/helm/
helm install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  --set image.repository=ghcr.io/<your-org>/agentic-autoscaler \
  --set image.tag=0.1.0 \
  --set aiProvider.provider=rules \
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

