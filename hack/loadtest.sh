#!/usr/bin/env bash
# loadtest.sh — drive a load cycle against the sample service and watch the
# AgenticAutoscaler react: ramp up → hold → stop → scale back down.
#
# Requires the load generator deployed in-cluster:  make loadgen-deploy
# It talks to the generator through a throwaway in-cluster curl pod, so it works
# the same on Linux, macOS and Windows (Git Bash) — no port-forward needed.
#
# Usage:
#   bash hack/loadtest.sh                 # defaults: 250 rps, hold 5 min
#   RPS=400 HOLD=180 bash hack/loadtest.sh
#   TARGET=http://my-svc.my-ns:8080 RPS=300 bash hack/loadtest.sh
set -euo pipefail

RPS="${RPS:-250}"
HOLD="${HOLD:-300}"
ERRPCT="${ERRPCT:-0}"
DELAYMS="${DELAYMS:-0}"
TARGET="${TARGET:-http://payment-service.production:9898}"
CURL_IMG="${CURL_IMG:-curlimages/curl:8.7.1}"
LOADGEN_NS="${LOADGEN_NS:-loadgen}"
LOADGEN_URL="http://loadgen.${LOADGEN_NS}:8080"

ctl() {
  # Run a curl command inside the cluster against the loadgen control API.
  kubectl run "loadctl-$(date +%s)-$RANDOM" --rm -i --restart=Never \
    -n "$LOADGEN_NS" --image="$CURL_IMG" -- "$@"
}

if ! kubectl -n "$LOADGEN_NS" get deploy loadgen >/dev/null 2>&1; then
  echo "✗ load generator not found in namespace '$LOADGEN_NS'."
  echo "  Deploy it first:  make loadgen-deploy"
  exit 1
fi

echo "→ Starting load: target=$TARGET rps=$RPS errorPct=$ERRPCT delayMs=$DELAYMS"
ctl curl -s -X POST "$LOADGEN_URL/start" -H 'Content-Type: application/json' \
  -d "{\"targetURL\":\"$TARGET\",\"rps\":$RPS,\"durationSec\":$((HOLD + 60)),\"errorPct\":$ERRPCT,\"delayMs\":$DELAYMS}" || true
echo
echo "  Watch replicas climb in another terminal:"
echo "    kubectl -n production get deploy payment-service -w"
echo "    kubectl -n production get agenticautoscaler -w"
echo
echo "→ Holding load for ${HOLD}s ..."
sleep "$HOLD"

echo "→ Stopping load — replicas should scale back down after cooldown."
ctl curl -s -X POST "$LOADGEN_URL/stop" || true
echo
echo "✓ Done. Keep watching the deployment to see it return to baseline."
