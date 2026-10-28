#!/usr/bin/env bash
# Timeout-storm demo driver — the Makefile demo targets without needing `make`
# (Git Bash on Windows ships without it). DEMO / TESTING ONLY: never run this
# against a production cluster.
#
# Usage (after `bash hack/bootstrap.sh`):
#   bash hack/demo.sh setup        # build + deploy faultgen and loadgen, autoscaler LIVE
#   bash hack/demo.sh reset        # clear any storm, wait until faultgen is back at baseline
#   bash hack/demo.sh storm-up     # >2 s latency + "connection pool exhausted" logs + traffic
#   bash hack/demo.sh storm-down   # clear the storm and stop traffic
#   bash hack/demo.sh status       # replicas + latest decision
#   bash hack/demo.sh watch        # status every 10 s (Ctrl-C to stop)
#   bash hack/demo.sh advisor-on   # KEDA scales faultgen on CPU + the agent's metric
#   bash hack/demo.sh advisor-off  # back to owner mode (the operator patches replicas)
#
# Knobs: RPS (default 100), DURATION seconds (600), QUIET scale-down window in
# minutes (2), MAX_REPLICAS (10).
set -euo pipefail

CYAN='\033[0;36m'; GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}  ✓ $*${NC}"; }
warn() { echo -e "${YELLOW}  ⚠ $*${NC}"; }
die()  { echo -e "${RED}  ✗ $*${NC}"; exit 1; }

NS=production
KIND_CLUSTER=agentic-autoscaler-dev
CURL_IMG=curlimages/curl:8.7.1
RPS="${RPS:-100}"
DURATION="${DURATION:-600}"
QUIET="${QUIET:-2}"
MAX_REPLICAS="${MAX_REPLICAS:-10}"

# bootstrap.sh installs kind here when it is missing.
export PATH="$HOME/.local/bin:$PATH"

cd "$(dirname "$0")/.."

loadctl() {  # loadctl <curl args...> — call the in-cluster loadgen control API
    kubectl run "loadctl-$(date +%s)" --rm -i --restart=Never -n loadgen \
        --image="$CURL_IMG" --quiet -- curl -s "$@"
    echo
}

replicas() { kubectl -n "$NS" get deploy faultgen -o jsonpath='{.spec.replicas}'; }
min_replicas() { kubectl -n "$NS" get agenticautoscaler faultgen-autoscaler -o jsonpath='{.spec.minReplicas}'; }

status() {
    local ready reason mode
    ready=$(kubectl -n "$NS" get deploy faultgen -o jsonpath='{.status.readyReplicas}/{.spec.replicas}')
    reason=$(kubectl -n "$NS" get agenticautoscaler faultgen-autoscaler \
        -o jsonpath='{.status.lastDecisionReason}')
    mode=$(kubectl -n "$NS" get agenticautoscaler faultgen-autoscaler \
        -o jsonpath='{.spec.hpaCoexistence.mode}{" rec="}{.status.recommendedReplicas}')
    [[ "$mode" == advisor* ]] || mode=""
    echo -e "$(date +%H:%M:%S)  faultgen ${CYAN}${ready}${NC} ready ${mode:+(${mode}) }│  ${reason:0:150}"
}

setup() {
    kubectl get crd agenticautoscalers.scaling.autoscaler.io >/dev/null 2>&1 \
        || die "Operator not installed — run: bash hack/bootstrap.sh"

    echo "  → Building demo images and loading them into kind..."
    docker build -q -f Dockerfile.faultgen -t agentic-autoscaler-faultgen:dev .
    docker build -q -f Dockerfile.loadgen -t agentic-autoscaler-loadgen:dev .
    kind load docker-image agentic-autoscaler-faultgen:dev agentic-autoscaler-loadgen:dev \
        --name "$KIND_CLUSTER"

    kubectl apply -f deploy/faultgen/faultgen.yaml
    kubectl apply -f config/samples/dev-faultgen-autoscaler.yaml
    kubectl apply -f deploy/loadgen/loadgen.yaml
    kubectl -n "$NS" rollout status deploy/faultgen --timeout=120s
    kubectl -n loadgen rollout status deploy/loadgen --timeout=120s

    # The sample starts in dry-run; the demo needs real scaling, sole ownership
    # (no HPA on faultgen) and a short quiet window so scale-down shows in minutes.
    kubectl -n "$NS" patch agenticautoscaler faultgen-autoscaler --type merge -p \
        "{\"spec\":{\"dryRun\":false,\"maxReplicas\":${MAX_REPLICAS},\"hpaCoexistence\":{\"mode\":\"owner\"},\"detection\":{\"scaleDownQuietWindowMinutes\":${QUIET}}}}"
    ok "faultgen-autoscaler is LIVE (owner, max ${MAX_REPLICAS}, ${QUIET}-min quiet window)"
    echo "  Next: bash hack/demo.sh reset   then   bash hack/demo.sh storm-up"
}

storm_up() {
    # Set the fault via env + rollout so EVERY replica faults from boot — including
    # any the operator scales up (a per-pod API toggle would miss new pods).
    kubectl -n "$NS" set env deploy/faultgen FAULT_ENABLED=true
    kubectl -n "$NS" rollout status deploy/faultgen --timeout=120s
    loadctl -X POST http://loadgen.loadgen:8080/start -H 'Content-Type: application/json' \
        -d "{\"targetURL\":\"http://faultgen.production:8080\",\"rps\":${RPS},\"durationSec\":${DURATION},\"errorPct\":0,\"delayMs\":0}"
    ok "Storm injected. p99 crosses 2 s within ~1-2 min, then the operator scales up."
    echo "  Watch: bash hack/demo.sh watch   (Grafana: Agentic Autoscaler — Decision Audit)"
}

storm_down() {
    kubectl -n "$NS" set env deploy/faultgen FAULT_ENABLED=false
    loadctl -X POST http://loadgen.loadgen:8080/stop
    ok "Storm cleared, load stopped. Scale-down follows after the ${QUIET}-min quiet window."
}

reset() {
    storm_down
    kubectl -n "$NS" rollout status deploy/faultgen --timeout=120s >/dev/null
    # The [2m] metric windows and the log lookback still hold the old storm right
    # after it clears, so wait them out as well as the replica count.
    local min started now
    min=$(min_replicas)
    started=$(date +%s)
    echo "  → Waiting for faultgen to settle at ≤ $((min + 1)) replicas (up to 10 min)..."
    while :; do
        now=$(date +%s)
        if [ $((now - started)) -ge 180 ] && [ "$(replicas)" -le $((min + 1)) ]; then
            break
        fi
        [ $((now - started)) -ge 600 ] && die "faultgen did not settle — check: bash hack/demo.sh status"
        status
        sleep 15
    done
    status
    ok "Baseline reached — ready for: bash hack/demo.sh storm-up"
}

advisor_on() {
    if ! kubectl get crd scaledobjects.keda.sh >/dev/null 2>&1; then
        echo "  → Installing KEDA (pinned 2.15.1) into namespace keda..."
        helm repo add kedacore https://kedacore.github.io/charts >/dev/null 2>&1 || true
        helm repo update kedacore >/dev/null
        helm upgrade --install keda kedacore/keda --version 2.15.1 \
            -n keda --create-namespace --wait --timeout 5m >/dev/null
    fi
    kubectl -n "$NS" patch agenticautoscaler faultgen-autoscaler --type merge \
        -p '{"spec":{"hpaCoexistence":{"mode":"advisor"}}}'
    kubectl apply -f deploy/advisor/faultgen-scaledobject.yaml
    ok "Advisor mode: the operator publishes a recommendation, KEDA's HPA scales on max(CPU, recommendation)."
    echo "  Inspect: kubectl -n $NS get hpa keda-hpa-faultgen   (two metrics, one controller)"
}

advisor_off() {
    # Deleting the ScaledObject removes the HPA KEDA created; replicas stay where
    # they are until the operator, now the sole owner again, decides.
    kubectl delete -f deploy/advisor/faultgen-scaledobject.yaml --ignore-not-found
    kubectl -n "$NS" patch agenticautoscaler faultgen-autoscaler --type merge \
        -p '{"spec":{"hpaCoexistence":{"mode":"owner"}}}'
    ok "Owner mode restored: the operator patches faultgen replicas directly."
}

case "${1:-}" in
    setup)      setup ;;
    advisor-on)  advisor_on ;;
    advisor-off) advisor_off ;;
    reset)      reset ;;
    storm-up)   storm_up ;;
    storm-down) storm_down ;;
    status)     status ;;
    watch)      while :; do status; sleep 10; done ;;
    *)          sed -n '2,18p' "$0"; exit 1 ;;
esac