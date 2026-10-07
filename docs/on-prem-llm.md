# Running fully on-prem with an open-source LLM

You do not need a cloud account, an API key, or any closed model to run the
agentic autoscaler. Explainable, AI-assisted scaling can run entirely inside
your cluster on a small open-source model — or with no model at all.

## Three reasoning tiers (pick what your policy allows)

| Tier | Provider | Needs | Explainability |
|------|----------|-------|----------------|
| **Rules** (default fallback) | built-in rule engine | nothing — always available, offline | deterministic reason strings (e.g. "connection pool exhausted with p99 latency spike") |
| **On-prem AI** | **Ollama** + a small local model | one node with ~4–6 GB RAM, no GPU | natural-language reason from a model you host |
| Cloud AI | Anthropic / OpenAI | an API key (billed) | natural-language reason from a cloud model |

The rule engine is always the safety net: **any** AI failure (unreachable model,
bad output, timeout) falls back to it, so the operator never blocks or crashes.
This means you can adopt the on-prem AI tier with zero risk — the worst case is a
deterministic rule-based decision.

## Enable on-prem AI (Ollama) with a small model

The bundled Ollama sub-chart runs a small (~4B) model that fits modest hardware —
the default is **`qwen2.5:3b`** (~4 GB RAM, CPU-only is fine).

```sh
helm upgrade --install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  --set aiProvider.provider=ollama \
  --set ollama.enabled=true \
  --set ollama.model=qwen2.5:3b
```

That's it — no secret, no external network egress for reasoning. The operator
talks to `http://ollama.ollama.svc.cluster.local:11434` in-cluster.

### Choosing a model (all run on modest hardware)

| Model | Params | Approx RAM | Notes |
|-------|--------|-----------|-------|
| `qwen2.5:3b` (default) | 3.1B | ~4 GB | strong instruction-following + JSON for its size |
| `qwen3:4b` | 4B | ~5 GB | newer; disable its thinking mode for clean JSON |
| `llama3.2:3b` | 3.2B | ~4 GB | solid general small model |
| `gemma2:2b` | 2.6B | ~3 GB | smallest; good for very constrained nodes |
| `llama3:8b` / `mistral:7b` | 7–8B | ~8–12 GB | better reasoning, more RAM |

Set any of them with `--set ollama.model=<name>` (or `OLLAMA_MODEL` when running
the operator directly). The operator pulls the model on first use.

## GPU vs CPU (and when to use the cloud instead)

Ollama uses a GPU automatically when it can see one, and runs on CPU otherwise.
Pick the path that matches your hardware — **GPU is preferred when available, CPU
is the portable fallback, and a cloud model is the escape hatch when neither is
fast enough**:

| Your setup | Path | How |
|------------|------|-----|
| **Desktop/laptop GPU** (e.g. an 8 GB RTX card) | **Host-native Ollama + GPU** — *simplest, recommended* | Run `ollama serve` on the host (it uses the GPU directly); point the operator at it. No in-cluster GPU plumbing, no in-cluster model pull. |
| **A real GPU Kubernetes cluster** | **In-cluster Ollama + GPU** | Enable the GPU request in the chart. Requires the NVIDIA device plugin + a GPU node. |
| **No GPU** | **In-cluster Ollama, CPU** (default) | A small (~3–4B) model on CPU — portable, slower. |
| **No GPU and CPU too slow** | **Cloud** | `aiProvider.provider=anthropic` (or `openai`) — the rule engine still backs it. |

### Host-native Ollama with a GPU (recommended for a dev box)

```sh
# On the host (uses your GPU automatically):
ollama serve &
ollama pull qwen2.5:3b        # or a bigger model your VRAM allows

# Install the operator pointing at the host Ollama (no in-cluster Ollama):
helm upgrade --install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  --set aiProvider.provider=ollama \
  --set ollama.enabled=false \
  --set ollama.baseURL=http://host.docker.internal:11434 \
  --set ollama.model=qwen2.5:3b
```

`hack/bootstrap.sh` does this for you when you set `AI_PROVIDER=ollama` — it
detects `nvidia-smi`, points the operator at `host.docker.internal:11434`, and
tells you which model to pull. (On Linux, if `host.docker.internal` doesn't
resolve from the cluster, use the host's bridge IP or run Ollama in-cluster.)

### In-cluster Ollama with a GPU

For a cluster that has a GPU node and the [NVIDIA device plugin](https://github.com/NVIDIA/k8s-device-plugin):

```sh
helm upgrade --install agentic-autoscaler deploy/helm/ \
  --namespace agentic-autoscaler-system --create-namespace \
  -f deploy/helm/values-gpu.yaml
```

This requests `nvidia.com/gpu` for the Ollama Pod (set `ollama.gpu.runtimeClassName`
if your cluster needs a GPU runtime class). See `deploy/helm/values-gpu.yaml`.

### Picking a model for your VRAM

| VRAM | Comfortable model |
|------|-------------------|
| ~2–3 GB | `gemma2:2b`, `qwen2.5:3b` |
| ~4–6 GB | `qwen2.5:3b`, `qwen3:4b`, `llama3.2:3b` |
| **~8 GB (e.g. RTX 5070)** | up to `qwen2.5:7b` / `llama3:8b` (quantized) — better reasoning |
| 12 GB+ | `llama3:8b`, `mistral:7b`, larger |

A scaling decision is a tiny structured output, so even the smallest models here
are capable; a bigger model mainly improves the nuance of the `reason` text.

## Why small models work here reliably

A scaling decision is a tiny, well-structured output — not open-ended prose — so a
3–4B model is more than capable. Two things keep it robust:

1. **Constrained output.** The request sets Ollama's `format: "json"` and
   `temperature: 0`, so the model emits a single deterministic JSON object rather
   than prose or markdown.
2. **Tolerant parsing.** If a small model still wraps the object in ```` ``` ````
   fences or a sentence of prose, the parser extracts the JSON object anyway.
3. **Fail-safe.** If the output is still unusable (or the model is down), the
   agent falls back to the rule engine — the decision is never lost.

The decision the model must produce:

```json
{"action":"scale-up","targetReplicas":8,"confidence":0.9,"reason":"one sentence for on-call SREs"}
```

## Switching backends later

Move a single autoscaler between backends without touching the workload — see the
`switch-backend` helper (`/switch-backend <namespace>/<name> <cloud|ollama>`), or
just re-run `helm upgrade` with a different `aiProvider.provider`. Because the
rule engine is always the fallback, switching is safe at any time.

## Air-gapped clusters

For a cluster with no internet egress, pre-pull the model image/weights into your
registry/node and point `ollama.image.repository` at your mirror. The operator
itself makes no outbound calls when `aiProvider.provider` is `ollama` or unset —
only in-cluster traffic to Ollama, Prometheus, and Loki.
