# Results — timeout-storm A/B (n=1)

**Single-run demonstration on a local kind cluster, not a benchmark.** Numbers
come straight from `data/hpa.csv` and `data/agentic.csv`; see `README.md` for
method and caveats. Regenerate with `python experiments/analyze.py`.

![timeline](results/timeline.png)

## Headline

- **Scaling:** the agentic autoscaler scaled at 32 s; the CPU-HPA never scaled during the run.
- **Why the HPA didn't react:** CPU stayed at most **12.8%** of request
  — well under the 70% target — because the storm is a latency/log incident, not a
  CPU one. A CPU-only autoscaler is blind to it.
- **Why the agentic autoscaler did react:** it fused the Prometheus p99 spike
  (peaked at **2990 ms**, past the 2000 ms rule-2 threshold) with the
  `connection-pool-exhausted` Loki pattern.

## Measured

| metric | CPU-HPA arm | agentic arm |
|--------|-------------|-------------|
| first scale-up | never (no scale action) | 32 s |
| max replicas | 2 | 10 |
| max p99 (ms) | 2990 | 2990 |
| max CPU (% of request) | 12.8 | — |

Reasoning provider during the run: **rule-based** (the Anthropic key had no
credits, so the cloud agent fell through to the deterministic rule-based agent —
rule 2 is deterministic, so the result is reproducible offline).
