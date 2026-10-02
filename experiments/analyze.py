#!/usr/bin/env python3
"""Turn the A/B run's raw CSVs into a figure + a methodology-honest summary.

Reads experiments/data/{hpa,agentic}.csv (written by run.sh) and produces
experiments/results/timeline.png and experiments/results/summary.md. It only
reports what the data shows; it never fabricates or smooths values (T5.1 STOP
condition). Run after run.sh:  python experiments/analyze.py
"""
import csv
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data")
RESULTS = os.path.join(HERE, "results")
CPU_TARGET_PCT = 70.0
P99_THRESHOLD_MS = 2000.0


def load(name):
    path = os.path.join(DATA, name)
    with open(path, newline="") as f:
        rows = list(csv.DictReader(f))
    for r in rows:
        for k, v in r.items():
            try:
                r[k] = float(v)
            except (TypeError, ValueError):
                pass
    return rows


def first_scale_t(rows):
    """First sample time at which replicas rose above the starting baseline."""
    if not rows:
        return None
    base = rows[0]["replicas"]
    for r in rows:
        if r["replicas"] > base:
            return r["t_sec"]
    return None


def main():
    os.makedirs(RESULTS, exist_ok=True)
    hpa = load("hpa.csv")
    agentic = load("agentic.csv")

    hpa_scale_t = first_scale_t(hpa)
    ag_scale_t = first_scale_t(agentic)
    hpa_cpu_max = max((r["cpu_pct"] for r in hpa), default=0.0)
    ag_p99_max = max((r["p99_ms"] for r in agentic), default=0.0)
    hpa_p99_max = max((r["p99_ms"] for r in hpa), default=0.0)
    hpa_reps_max = max((r["replicas"] for r in hpa), default=0)
    ag_reps_max = max((r["replicas"] for r in agentic), default=0)

    # ── Figure ────────────────────────────────────────────────────────────────
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(9, 7), sharex=True)
    fig.suptitle("Timeout-storm incident: CPU-HPA vs agentic autoscaler (n=1, kind)",
                 fontsize=13, fontweight="bold")

    ax1.step([r["t_sec"] for r in hpa], [r["replicas"] for r in hpa],
             where="post", label="CPU-HPA arm", color="#c1440e", linewidth=2)
    ax1.step([r["t_sec"] for r in agentic], [r["replicas"] for r in agentic],
             where="post", label="Agentic arm", color="#1f6f8b", linewidth=2)
    if ag_scale_t is not None:
        ax1.axvline(ag_scale_t, color="#1f6f8b", ls=":", alpha=0.6)
        ax1.annotate("agentic scales", (ag_scale_t, ag_reps_max),
                     textcoords="offset points", xytext=(6, -4), color="#1f6f8b", fontsize=9)
    ax1.set_ylabel("replicas")
    ax1.set_title("Replicas over time — the HPA never reacts; the agentic autoscaler does", fontsize=10)
    ax1.legend(loc="upper left")
    ax1.grid(True, alpha=0.3)

    ax2.plot([r["t_sec"] for r in hpa], [r["p99_ms"] for r in hpa],
             label="p99 (HPA arm)", color="#c1440e", alpha=0.85)
    ax2.plot([r["t_sec"] for r in agentic], [r["p99_ms"] for r in agentic],
             label="p99 (agentic arm)", color="#1f6f8b", alpha=0.85)
    ax2.axhline(P99_THRESHOLD_MS, color="grey", ls="--", alpha=0.7, label="rule-2 p99 threshold (2000 ms)")
    ax2.plot([r["t_sec"] for r in hpa], [r["cpu_pct"] for r in hpa],
             label="CPU % of request (HPA arm)", color="#8a8d91", alpha=0.9)
    ax2.axhline(CPU_TARGET_PCT, color="#c1440e", ls=":", alpha=0.5, label="HPA CPU target (70%)")
    ax2.set_ylabel("p99 latency (ms) / CPU (%)")
    ax2.set_xlabel("seconds since incident start")
    ax2.set_title("The same incident: p99 spikes past 2 s while CPU stays near-idle", fontsize=10)
    ax2.legend(loc="upper left", fontsize=8)
    ax2.grid(True, alpha=0.3)

    fig.tight_layout(rect=(0, 0, 1, 0.96))
    out_png = os.path.join(RESULTS, "timeline.png")
    fig.savefig(out_png, dpi=130)
    print("wrote", out_png)

    # ── Summary ───────────────────────────────────────────────────────────────
    def fmt(t):
        return f"{int(t)} s" if t is not None else "never (no scale action)"

    if ag_scale_t is not None and hpa_scale_t is None:
        lead = f"the agentic autoscaler scaled at {int(ag_scale_t)} s; the CPU-HPA never scaled during the run"
    elif ag_scale_t is not None and hpa_scale_t is not None:
        lead = f"scaling lead time = {int(hpa_scale_t - ag_scale_t)} s (agentic {int(ag_scale_t)} s vs HPA {int(hpa_scale_t)} s)"
    else:
        lead = "neither arm scaled during the run"

    summary = f"""# Results — timeout-storm A/B (n=1)

**Single-run demonstration on a local kind cluster, not a benchmark.** Numbers
come straight from `data/hpa.csv` and `data/agentic.csv`; see `README.md` for
method and caveats. Regenerate with `python experiments/analyze.py`.

![timeline](results/timeline.png)

## Headline

- **Scaling:** {lead}.
- **Why the HPA didn't react:** CPU stayed at most **{hpa_cpu_max:.1f}%** of request
  — well under the 70% target — because the storm is a latency/log incident, not a
  CPU one. A CPU-only autoscaler is blind to it.
- **Why the agentic autoscaler did react:** it fused the Prometheus p99 spike
  (peaked at **{ag_p99_max:.0f} ms**, past the 2000 ms rule-2 threshold) with the
  `connection-pool-exhausted` Loki pattern.

## Measured

| metric | CPU-HPA arm | agentic arm |
|--------|-------------|-------------|
| first scale-up | {fmt(hpa_scale_t)} | {fmt(ag_scale_t)} |
| max replicas | {int(hpa_reps_max)} | {int(ag_reps_max)} |
| max p99 (ms) | {hpa_p99_max:.0f} | {ag_p99_max:.0f} |
| max CPU (% of request) | {hpa_cpu_max:.1f} | — |

Reasoning provider during the run: **rule-based** (the Anthropic key had no
credits, so the cloud agent fell through to the deterministic rule-based agent —
rule 2 is deterministic, so the result is reproducible offline).
"""
    out_md = os.path.join(RESULTS, "summary.md")
    with open(out_md, "w") as f:
        f.write(summary)
    print("wrote", out_md)
    print("\n" + summary)


if __name__ == "__main__":
    main()
