# Validation checklist — reproduce everything on a fresh machine

A tick-box run sheet to validate the operator end-to-end on another machine (e.g.
a GPU box), after `git pull` on `main`. Each step has the command and the result
you should see. Nothing here needs a cloud account — the deterministic rule
engine and a local model both work offline.

> Times are approximate. The signal queries use `[1m]`–`[2m]` rate windows, so
> allow ~1–2 minutes after a change before a decision reflects it.

## 0. Prerequisites

- [ ] Docker running; `kind`, `kubectl`, `helm` on PATH (or let `hack/bootstrap.sh` install kind/kubectl)
- [ ] Go ≥ 1.21 (`go version`)
- [ ] ~8 GB free RAM for Docker; a few GB free disk for images/model
- [ ] **GPU (optional):** `nvidia-smi` works on the host, and [Ollama](https://ollama.com) is installed

## 1. Build & unit tests (no cluster)

```sh
go build ./...
go test ./pkg/... ./cmd/... ./internal/controller/ -run 'Test[^C]' -count=1   # fake-client + pure tests
helm lint deploy/helm/
```

- [ ] `go build` succeeds
- [ ] tests pass (the envtest `TestControllers` suite needs `KUBEBUILDER_ASSETS`; CI runs it — `make test` provisions it locally)
- [ ] helm lint reports `0 chart(s) failed`

## 2. Bring up the stack

```sh
bash hack/bootstrap.sh            # kind + Prometheus/Loki/Grafana + operator (dry-run)
kubectl -n agentic-autoscaler-system get pods
kubectl -n monitoring get pods | grep -E 'prometheus|loki|grafana'
```

- [ ] operator Pod is `Running`
- [ ] Prometheus, Loki, Grafana Pods are `Running`

## 3. Choose a reasoning backend

Pick ONE. The rule engine is always the fallback, so any of these is safe.

### 3a. Local LLM on a GPU (recommended for a GPU box)

```sh
ollama serve &                    # uses the GPU automatically
ollama pull qwen2.5:3b            # ~2 GB; or a bigger model your VRAM allows
AI_PROVIDER=ollama bash hack/bootstrap.sh    # detects nvidia-smi, points operator at host Ollama
```

- [ ] bootstrap prints `GPU detected (nvidia-smi) — host-native Ollama will use it automatically`
- [ ] operator env shows `AI_PROVIDER=ollama` and `OLLAMA_BASE_URL=http://host.docker.internal:11434`
      (`kubectl -n agentic-autoscaler-system set env deploy/agentic-autoscaler-agentic-autoscaler --list | grep OLLAMA`)

### 3b. Local LLM on CPU

Same as 3a without a GPU — expect the "No GPU detected … CPU" message. Use a small model (`qwen2.5:3b`, `gemma2:2b`).

### 3c. Cloud (Anthropic) or 3d. rules-only

```sh
export ANTHROPIC_API_KEY=sk-ant-...   # 3c: a funded key; omit for 3d (rules-only)
bash hack/bootstrap.sh
```

- [ ] with no key, operator logs show `provider: rule-based` decisions (never blocks)

## 4. Scale-UP demo (log-pattern-driven) — the flagship

```sh
make demo-timeout-storm           # faultgen + loadgen + CR (dry-run)
make storm-up                     # inject >2s latency + pool-exhausted logs
# wait ~2 min for the p99 window to fill, then:
kubectl -n production get agenticautoscaler faultgen-autoscaler -o jsonpath='{.status.lastDecisionReason}{"\n"}'
```

- [ ] decision reason contains **`connection pool exhausted with p99 latency spike`**
- [ ] it was driven by the **log pattern**, not error rate (audit `patternsMatched` includes `connection-pool-exhausted`):
  ```sh
  kubectl -n agentic-autoscaler-system get cm decision-audit-log \
    -o jsonpath='{.data.decisions\.json}' | grep -o 'connection-pool-exhausted' | head -1
  ```
- [ ] **Live scaling** (optional): `make storm-live` then `make storm-up` → `kubectl -n production get deploy faultgen -w` shows replicas climb (e.g. 2 → 3)

## 5. Scale-DOWN demo (the other half)

```sh
make demo-scale-down QUIET=2      # owner+live, 2-minute quiet window
make storm-up                     # drive it up first
make storm-down                   # clear the storm → signals go quiet
kubectl -n production get deploy faultgen -w
```

- [ ] after ~2 min of quiet + cooldown, replicas fall back toward `minReplicas + 1`
- [ ] the scale-down decision reason mentions releasing capacity

## 6. A/B experiment (HPA vs agentic) — regenerate the figure

```sh
bash experiments/run.sh           # ~12 min: both arms, writes experiments/data/*.csv
python experiments/analyze.py     # writes experiments/results/timeline.png + summary.md
```

- [ ] `experiments/results/summary.md`: CPU-HPA `first scale-up = never`, agentic ≈ 30s, agentic max replicas > 2
- [ ] `timeline.png` shows the HPA flat while the agentic line steps up
- [ ] (this is n=1, a demonstration — the committed figure is from one such run)

## 7. Bring-your-own infra — preflight

With write mode on (`--set console.writeEnabled=true`) and the console port-forwarded:

```sh
curl -s -X POST http://localhost:8090/api/preflight \
  -H 'Content-Type: application/json' \
  -d '{"prometheusURL":"http://prometheus-operated.monitoring:9090","promQuery":"up",
       "lokiURL":"http://loki.monitoring:3100","lokiQuery":"{namespace=\"production\"}"}'
```

- [ ] response shows `"prometheus":{"ok":true,...}` and `"loki":{"ok":true,...}`
- [ ] pointing at a bad URL returns `"ok":false` with an `unreachable …` detail

## 8. Explainability

```sh
kubectl -n agentic-autoscaler-system port-forward svc/agentic-autoscaler-agentic-autoscaler 8090:8090
# open http://localhost:8090 — Explore tab shows the decision log with reasons
```

- [ ] every decision carries a human-readable `reason`
- [ ] (if Grafana configured) real scale actions show up as dashboard annotations

## 9. Teardown

```sh
make storm-down
kind delete cluster --name agentic-autoscaler-dev
```

- [ ] cluster removed

---

**If something doesn't tick:** the operator fails safe — a provider/signal error
degrades to the rule engine or a `hold`, never a crash. Check
`kubectl -n agentic-autoscaler-system logs deploy/agentic-autoscaler-agentic-autoscaler`,
and for signals, use the preflight (§7) to confirm Prometheus/Loki are reachable
and the queries return data.
