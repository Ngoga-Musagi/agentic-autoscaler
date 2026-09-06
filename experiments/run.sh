#!/usr/bin/env bash
# A/B measurement harness for the timeout-storm incident (T5.1).
#
# Runs the SAME injected incident against two autoscalers, one at a time, and
# records replicas / p99 latency / CPU over time for each:
#
#   Arm A — CPU HorizontalPodAutoscaler only (target 70%).
#   Arm B — the agentic autoscaler in owner mode (log-pattern + latency fusion).
#
# The incident is faultgen's timeout storm: >2 s latency + "connection pool
# exhausted" logs, with CPU staying near-idle. A CPU HPA cannot see this; the
# agentic autoscaler can. Output is raw CSV under experiments/data/ — analyze.py
# turns it into a figure + summary. This is a single-run demonstration (n=1), not
# a benchmark; never hand-edit the CSVs (see README.md).
#
# Prereqs: a running kind stack (bash hack/bootstrap.sh), faultgen + loadgen
# deployed (make demo-timeout-storm), and metrics-server (the script installs it
# if missing). Run from the repo root:  bash experiments/run.sh
#
# Note: -e is deliberately NOT set — a single flaky kubectl/Prometheus call in the
# sampling loop must record a default and continue, never abort a run midway.
set -uo pipefail

NS=production
TARGET=faultgen
LOADGEN=http://loadgen.loadgen:8080
RPS="${RPS:-100}"
DURATION="${DURATION:-180}"   # seconds sampled per arm
INTERVAL="${INTERVAL:-15}"    # sample cadence
CURL_IMG=curlimages/curl:8.7.1

DIR="$(cd "$(dirname "$0")" && pwd)"
DATA="$DIR/data"
mkdir -p "$DATA"

say() { echo -e "\n=== $* ==="; }

# ── Prometheus access: one host port-forward for the whole run ───────────────
PROM_LOCAL="http://localhost:19090"
kubectl -n monitoring port-forward svc/prometheus-operated 19090:9090 >/dev/null 2>&1 &
PF_PID=$!
cleanup() { kill "$PF_PID" 2>/dev/null || true; }
trap cleanup EXIT
sleep 4

promq() { # promq '<promql>' -> scalar value or empty
  curl -s --get "$PROM_LOCAL/api/v1/query" --data-urlencode "query=$1" 2>/dev/null \
    | grep -oE '"value":\[[0-9.]+,"[^"]+"\]' | grep -oE ',"[0-9.eE+-]+"\]$' | tr -d ',"]' | head -1
}

P99Q="histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{service=\"$TARGET\"}[1m])))"
CPUQ="sum(rate(container_cpu_usage_seconds_total{pod=~\"$TARGET-.*\",container=\"$TARGET\"}[1m]))"
CPU_REQUEST_CORES=0.05  # faultgen requests 50m; used to express CPU as % of request

storm_on()  { kubectl -n "$NS" set env deploy/"$TARGET" FAULT_ENABLED=true  >/dev/null
              kubectl -n "$NS" rollout status deploy/"$TARGET" --timeout=120s >/dev/null
              kubectl run loadctl-$(date +%s) --rm -i --restart=Never -n loadgen --image="$CURL_IMG" -- \
                curl -s -X POST "$LOADGEN/start" -H 'Content-Type: application/json' \
                -d "{\"targetURL\":\"http://$TARGET.$NS:8080\",\"rps\":$RPS,\"durationSec\":$((DURATION+120)),\"errorPct\":0,\"delayMs\":0}" >/dev/null 2>&1 || true; }
storm_off() { kubectl run loadctl-$(date +%s) --rm -i --restart=Never -n loadgen --image="$CURL_IMG" -- \
                curl -s -X POST "$LOADGEN/stop" >/dev/null 2>&1 || true
              kubectl -n "$NS" set env deploy/"$TARGET" FAULT_ENABLED=false >/dev/null; }

DRAIN="${DRAIN:-75}"   # seconds of quiet so the p99 [1m] window empties between arms
reset_target() { # scale back to baseline, clear fault, and drain the latency window
  kubectl -n "$NS" scale deploy/"$TARGET" --replicas=2 >/dev/null
  kubectl -n "$NS" set env deploy/"$TARGET" FAULT_ENABLED=false >/dev/null
  kubectl -n "$NS" rollout status deploy/"$TARGET" --timeout=120s >/dev/null
  # Drain: no fault, no load → the p99 rate window decays to ~0 so the next arm
  # starts from a genuine quiet baseline (both arms begin at 2 replicas, low p99).
  echo "  draining latency window (${DRAIN}s)…"
  sleep "$DRAIN"
}

# ── metrics-server (kind needs --kubelet-insecure-tls) ───────────────────────
if ! kubectl top pods -n "$NS" >/dev/null 2>&1; then
  say "installing metrics-server"
  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml >/dev/null
  kubectl -n kube-system patch deploy metrics-server --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]' >/dev/null
  kubectl -n kube-system rollout status deploy/metrics-server --timeout=180s >/dev/null
  sleep 20
fi

# Ensure only one autoscaler manages the target during a run.
kubectl -n "$NS" delete agenticautoscaler faultgen-autoscaler --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "$NS" delete agenticautoscaler faultgen-exp-autoscaler --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "$NS" delete hpa faultgen-exp-hpa --ignore-not-found >/dev/null 2>&1 || true

# ── Arm A: CPU HPA ───────────────────────────────────────────────────────────
say "Arm A — CPU HPA (target 70%)"
reset_target
kubectl -n "$NS" autoscale deploy/"$TARGET" --name=faultgen-exp-hpa --cpu-percent=70 --min=2 --max=10 >/dev/null
sleep 30   # let the HPA read metrics-server before the incident

echo "t_sec,replicas,autoscaler_target,cpu_pct,p99_ms" > "$DATA/hpa.csv"
storm_on
A_START=$(date +%s)
while [ $(( $(date +%s) - A_START )) -lt "$DURATION" ]; do
  t=$(( $(date +%s) - A_START ))
  reps=$(kubectl -n "$NS" get deploy "$TARGET" -o jsonpath='{.status.replicas}' 2>/dev/null); reps=${reps:-0}
  hpad=$(kubectl -n "$NS" get hpa faultgen-exp-hpa -o jsonpath='{.status.desiredReplicas}' 2>/dev/null); hpad=${hpad:-0}
  cpuc=$(promq "$CPUQ"); cpuc=${cpuc:-0}
  cpupct=$(awk -v c="$cpuc" -v r="$CPU_REQUEST_CORES" -v n="$reps" 'BEGIN{ if(n<1)n=1; printf "%.1f", (c/(n*r))*100 }')
  p99=$(promq "$P99Q"); p99ms=$(awk -v s="${p99:-0}" 'BEGIN{printf "%.0f", s*1000}')
  echo "$t,$reps,$hpad,$cpupct,$p99ms" >> "$DATA/hpa.csv"
  echo "  A t=${t}s reps=$reps hpaDesired=$hpad cpu=${cpupct}% p99=${p99ms}ms"
  sleep "$INTERVAL"
done
storm_off
kubectl -n "$NS" delete hpa faultgen-exp-hpa --ignore-not-found >/dev/null 2>&1 || true

# ── Arm B: agentic autoscaler (owner mode, live) ─────────────────────────────
say "Arm B — agentic autoscaler (owner, live)"
reset_target
kubectl apply -f "$DIR/agentic-owner.yaml" >/dev/null
sleep 5

echo "t_sec,replicas,autoscaler_target,cpu_pct,p99_ms,scaled,provider" > "$DATA/agentic.csv"
storm_on
B_START=$(date +%s)
prev_reps=2
while [ $(( $(date +%s) - B_START )) -lt "$DURATION" ]; do
  t=$(( $(date +%s) - B_START ))
  reps=$(kubectl -n "$NS" get deploy "$TARGET" -o jsonpath='{.status.replicas}' 2>/dev/null); reps=${reps:-0}
  crrep=$(kubectl -n "$NS" get agenticautoscaler faultgen-exp-autoscaler -o jsonpath='{.status.currentReplicas}' 2>/dev/null); crrep=${crrep:-$reps}
  prov=$(kubectl -n "$NS" get agenticautoscaler faultgen-exp-autoscaler -o jsonpath='{.status.lastDecisionReason}' 2>/dev/null | grep -qi "connection pool" && echo rule-based || echo -)
  cpuc=$(promq "$CPUQ"); cpuc=${cpuc:-0}
  cpupct=$(awk -v c="$cpuc" -v r="$CPU_REQUEST_CORES" -v n="$reps" 'BEGIN{ if(n<1)n=1; printf "%.1f", (c/(n*r))*100 }')
  p99=$(promq "$P99Q"); p99ms=$(awk -v s="${p99:-0}" 'BEGIN{printf "%.0f", s*1000}')
  scaled=0; if [ "${reps:-0}" -gt "${prev_reps:-0}" ] 2>/dev/null; then scaled=1; fi; prev_reps=$reps
  echo "$t,$reps,$crrep,$cpupct,$p99ms,$scaled,$prov" >> "$DATA/agentic.csv"
  echo "  B t=${t}s reps=$reps cpu=${cpupct}% p99=${p99ms}ms scaled=$scaled"
  sleep "$INTERVAL"
done
storm_off

# ── Restore the demo state (T4.1 CR, dry-run) ────────────────────────────────
say "restoring demo state"
kubectl -n "$NS" delete agenticautoscaler faultgen-exp-autoscaler --ignore-not-found >/dev/null 2>&1 || true
reset_target
kubectl apply -f "$DIR/../config/samples/dev-faultgen-autoscaler.yaml" >/dev/null 2>&1 || true

say "done — data in experiments/data/. Run: python experiments/analyze.py"
