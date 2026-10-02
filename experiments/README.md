# Experiments — timeout-storm A/B

A reproducible, single-run demonstration that a **CPU-based HorizontalPodAutoscaler
misses a latency/log incident that the agentic autoscaler catches.** It produces
the evidence behind the poster's motivation and results.

> **This is n=1 — a demonstration, not a benchmark.** It shows a mechanism on one
> local `kind` cluster, not a statistical claim. The figure and `results/summary.md`
> are generated straight from the raw CSVs; nothing is hand-tuned (if a run is
> noisy, rerun it — never edit the data).

## The incident

`cmd/faultgen`'s timeout storm: every replica injects **> 2 s** response latency
and writes `connection pool exhausted` lines to stdout, while doing almost no CPU
work (it just `time.Sleep`s). So the incident is real in **p99 latency** and in
**logs**, but **invisible to CPU** — the exact blind spot of CPU autoscaling.

## The two arms

| Arm | Autoscaler | Sees |
|-----|------------|------|
| A | CPU HPA, target 70%, min 2 / max 10 | CPU only |
| B | AgenticAutoscaler, owner mode, live, min 2 / max 10 | Prometheus p99 **fused with** the Loki log pattern (rule 2) |

Both manage the **same** `faultgen` Deployment and are subjected to the **same**
incident configuration (100 rps, `FAULT_ENABLED=true`). The arms run **sequentially**
(one autoscaler at a time — two autoscalers on one Deployment would fight), with a
drain between them so each starts from a genuine quiet baseline: 2 replicas, p99 ≈ 0.

## Result (from the committed run)

![timeline](results/timeline.png)

- **HPA arm:** replicas stay flat at **2**. CPU peaks at ~13% of request — far below
  the 70% target — so the HPA never scales, even though p99 sits at ~3 s.
- **Agentic arm:** scales **2 → 3 → 5 → 8 → 10**, first action ~32 s in, driven by the
  p99 spike + `connection-pool-exhausted` pattern.

See `results/summary.md` for the exact numbers.

## Reproduce

Prerequisites: a running stack (`bash hack/bootstrap.sh`) and the demo workload
(`make demo-timeout-storm`). The script installs `metrics-server` (with kind's
`--kubelet-insecure-tls`) if the metrics API is missing.

```sh
bash experiments/run.sh          # ~12 min: runs both arms, writes data/*.csv
python experiments/analyze.py    # writes results/timeline.png + results/summary.md
```

Tunables (env): `DURATION` (s sampled per arm, default 180), `INTERVAL` (sample
cadence, 15), `RPS` (100), `DRAIN` (quiet seconds between arms, 75). The
experiment CR (`agentic-owner.yaml`) uses `[1m]` rate windows so p99 responds
within ~1 min; the run restores the T4.1 demo CR (dry-run) when it finishes.

## What is and isn't measured

- **Captured:** replicas over time, p99 latency, CPU (% of request), the agentic
  scale events, and — for context — the HPA's own view. Reasoning provider during
  the run was **rule-based** (the Anthropic key had no credits, so the cloud agent
  fell through to the deterministic rule-based agent; rule 2 is deterministic, so
  the result reproduces offline).
- **Not shown here:** the operator also exports `agentic_autoscaler_decision_latency_seconds`
  and `agentic_autoscaler_ai_provider_errors_total` (fallback rate) — available in
  Prometheus but not plotted in this figure.

## Caveats specific to the injector

- faultgen's fault is **synthetic and fixed** — adding replicas does not relieve
  the 2.5 s latency, so the agentic arm scales all the way to `maxReplicas`. In a
  real pool-exhaustion incident more replicas would add pool capacity and the
  latency would fall; here the point is only *which autoscaler reacts at all*.
- CPU is expressed as a percentage of faultgen's small 50m request, the same basis
  the HPA uses; the absolute CPU is tiny either way.
