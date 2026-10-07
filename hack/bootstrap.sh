#!/usr/bin/env bash
# Bootstrap the full agentic-autoscaler dev stack.
# Works in Git Bash (Windows) and WSL2 — no 'make' required.
# Auto-installs kind and kubectl if missing.
#
# Usage:
#   bash hack/bootstrap.sh                  # base stack (dry-run, read-only console)
#   WITH_LOADGEN=1 bash hack/bootstrap.sh   # + load generator, write-mode console, LIVE scaling
#
# Credentials are read from the shell environment, falling back to a .env file
# in the repo root if present:
#   ANTHROPIC_API_KEY=sk-ant-...   enables AI reasoning (falls back to rules if unset)
#   ANTHROPIC_MODEL=...            overrides the model (e.g. claude-sonnet-4-6)
#   GRAFANA_TOKEN=...              enables Grafana annotation push (optional)
set -euo pipefail

CYAN='\033[0;36m'; GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
step() { echo -e "\n${CYAN}━━ $* ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; }
ok()   { echo -e "${GREEN}  ✓ $*${NC}"; }
warn() { echo -e "${YELLOW}  ⚠ $*${NC}"; }
die()  { echo -e "${RED}  ✗ $*${NC}"; exit 1; }

OPERATOR_NS="agentic-autoscaler-system"
DEPLOY_NAME="agentic-autoscaler-agentic-autoscaler"   # chart fullname = release-chart
WITH_LOADGEN="${WITH_LOADGEN:-0}"

# ── Detect OS (Windows/Git Bash vs Linux/WSL2) ───────────────────────────────
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) OS=windows ;;
  Linux*)               OS=linux   ;;
  Darwin*)              OS=darwin  ;;
  *)                    OS=unknown ;;
esac

# Local bin dir on PATH for tools we download
LOCAL_BIN="$HOME/.local/bin"
mkdir -p "$LOCAL_BIN"
export PATH="$LOCAL_BIN:$PATH"

# ── Credential resolution (shell env wins, else .env, CR-safe) ────────────────
get_env_val() {  # get_env_val KEY -> value from .env (CR/quote-stripped) or empty
    [ -f .env ] || return 0
    # The trailing `|| true` keeps a missing key from tripping `set -e`/pipefail.
    grep -E "^[[:space:]]*$1=" .env 2>/dev/null | head -1 | cut -d= -f2- \
        | tr -d '\r' | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'\$//" || true
}
ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-$(get_env_val ANTHROPIC_API_KEY)}"
ANTHROPIC_MODEL="${ANTHROPIC_MODEL:-$(get_env_val ANTHROPIC_MODEL)}"
GRAFANA_TOKEN="${GRAFANA_TOKEN:-$(get_env_val GRAFANA_TOKEN)}"

# ── Tool installer helpers ───────────────────────────────────────────────────

install_kind() {
    local version="v0.23.0"
    echo "  → Downloading kind ${version} for ${OS}..."
    case "$OS" in
      windows)
        curl -fsSL "https://kind.sigs.k8s.io/dl/${version}/kind-windows-amd64" \
            -o "$LOCAL_BIN/kind.exe"
        ;;
      linux)
        curl -fsSL "https://kind.sigs.k8s.io/dl/${version}/kind-linux-amd64" \
            -o "$LOCAL_BIN/kind"
        chmod +x "$LOCAL_BIN/kind"
        ;;
      darwin)
        curl -fsSL "https://kind.sigs.k8s.io/dl/${version}/kind-darwin-amd64" \
            -o "$LOCAL_BIN/kind"
        chmod +x "$LOCAL_BIN/kind"
        ;;
    esac
    ok "kind installed → $LOCAL_BIN"
}

install_kubectl() {
    local version
    version=$(curl -fsSL https://dl.k8s.io/release/stable.txt)
    echo "  → Downloading kubectl ${version} for ${OS}..."
    case "$OS" in
      windows)
        curl -fsSL "https://dl.k8s.io/release/${version}/bin/windows/amd64/kubectl.exe" \
            -o "$LOCAL_BIN/kubectl.exe"
        ;;
      linux)
        curl -fsSL "https://dl.k8s.io/release/${version}/bin/linux/amd64/kubectl" \
            -o "$LOCAL_BIN/kubectl"
        chmod +x "$LOCAL_BIN/kubectl"
        ;;
      darwin)
        curl -fsSL "https://dl.k8s.io/release/${version}/bin/darwin/amd64/kubectl" \
            -o "$LOCAL_BIN/kubectl"
        chmod +x "$LOCAL_BIN/kubectl"
        ;;
    esac
    ok "kubectl installed → $LOCAL_BIN"
}

# Create/update a single-key secret WITHOUT clobbering an existing real value.
ensure_secret() {  # ensure_secret <name> <key> <value>
    local name="$1" key="$2" val="$3"
    if [ -n "$val" ] && [ "$val" != "not-configured" ]; then
        kubectl create secret generic "$name" --from-literal="$key=$val" \
            --namespace "$OPERATOR_NS" --dry-run=client -o yaml | kubectl apply -f -
        ok "$name set from supplied value"
    elif kubectl get secret "$name" -n "$OPERATOR_NS" >/dev/null 2>&1; then
        warn "$name already exists — leaving it unchanged (no value supplied)"
    else
        kubectl create secret generic "$name" --from-literal="$key=not-configured" \
            --namespace "$OPERATOR_NS" --dry-run=client -o yaml | kubectl apply -f -
        warn "$name created as placeholder (not-configured)"
    fi
}

# ── 0. Check / install prerequisites ─────────────────────────────────────────
step "0/7  Checking prerequisites"

# Docker
command -v docker &>/dev/null \
    || die "Docker not found. Install Docker Desktop and enable Git Bash / WSL2 integration."
docker info > /dev/null 2>&1 \
    || die "Docker daemon not running. Start Docker Desktop."
ok "docker  $(docker --version | awk '{print $3}' | tr -d ',')"

# kind
if ! command -v kind &>/dev/null; then
    warn "kind not found — installing..."
    install_kind
fi
ok "kind    $(kind version | awk '{print $2}')"

# kubectl
if ! command -v kubectl &>/dev/null; then
    warn "kubectl not found — installing..."
    install_kubectl
fi
ok "kubectl $(kubectl version --client -o json 2>/dev/null \
    | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("clientVersion",{}).get("gitVersion",""))' 2>/dev/null \
    || kubectl version --client --short 2>/dev/null | awk '{print $3}' \
    || echo '(installed)')"

# helm
command -v helm &>/dev/null \
    || die "helm not found. Install from https://helm.sh/docs/intro/install/"
ok "helm    $(helm version --short)"

# ── 1. Create kind cluster ────────────────────────────────────────────────────
step "1/7  Creating kind cluster: agentic-autoscaler-dev"

if kind get clusters 2>/dev/null | grep -q "^agentic-autoscaler-dev$"; then
    warn "Cluster already exists — reusing it"
else
    kind create cluster \
        --config hack/kind-config.yaml \
        --name agentic-autoscaler-dev
    ok "Cluster created"
fi

kubectl config use-context kind-agentic-autoscaler-dev
ok "kubectl context → kind-agentic-autoscaler-dev"

# ── 2. Install observability stack ───────────────────────────────────────────
step "2/7  Installing Prometheus + Loki + Grafana  (≈ 4 min)"

kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
helm repo add grafana              https://grafana.github.io/helm-charts              2>/dev/null || true
helm repo update

# Skip re-installing an already-present release: upgrading a working stack to a
# newer chart can break it (e.g. Grafana), and we only need it present + healthy.
if helm status kube-prometheus-stack -n monitoring >/dev/null 2>&1; then
    warn "kube-prometheus-stack already installed — reusing it (skipping upgrade)"
else
    echo "  → kube-prometheus-stack..."
    helm upgrade --install kube-prometheus-stack \
        prometheus-community/kube-prometheus-stack \
        --namespace monitoring \
        --set grafana.adminPassword=admin \
        --set grafana.sidecar.dashboards.enabled=true \
        --set grafana.sidecar.dashboards.label=grafana_dashboard \
        --set prometheus.prometheusSpec.podMonitorSelectorNilUsesHelmValues=false \
        --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
        --wait --timeout 6m
    ok "Prometheus + Grafana ready"
fi

if helm status loki -n monitoring >/dev/null 2>&1; then
    warn "loki already installed — reusing it (skipping upgrade)"
else
    echo "  → loki-stack..."
    helm upgrade --install loki grafana/loki-stack \
        --namespace monitoring \
        --set promtail.enabled=true \
        --set loki.persistence.enabled=false \
        --wait --timeout 4m
    ok "Loki ready"
fi

# loki-stack registers its Grafana datasource as the default, which collides
# with the Prometheus default and crash-loops Grafana. Demote Loki to non-default.
if kubectl -n monitoring get cm loki-loki-stack -o jsonpath='{.data.loki-stack-datasource\.yaml}' 2>/dev/null | grep -q 'isDefault: true'; then
    kubectl -n monitoring patch cm loki-loki-stack --type merge \
        -p '{"data":{"loki-stack-datasource.yaml":"apiVersion: 1\ndatasources:\n- name: Loki\n  type: loki\n  access: proxy\n  url: \"http://loki:3100\"\n  version: 1\n  isDefault: false\n  jsonData:\n    {}"}}' >/dev/null
    kubectl -n monitoring rollout restart deploy kube-prometheus-stack-grafana >/dev/null 2>&1 || true
    ok "Grafana datasource conflict fixed (Loki demoted from default)"
fi

echo "  → Loading decision-audit dashboard..."
kubectl create configmap agentic-autoscaler-dashboard \
    --from-file=agentic-autoscaler-decisions.json=deploy/grafana/dashboard.json \
    --namespace monitoring \
    --dry-run=client -o yaml | kubectl apply -f -
kubectl label configmap agentic-autoscaler-dashboard \
    grafana_dashboard=1 --namespace monitoring --overwrite
ok "Grafana dashboard loaded"

# ── 3. Build and load operator image ─────────────────────────────────────────
step "3/7  Building operator image  (≈ 2 min)"

docker build -t agentic-autoscaler:dev .
kind load docker-image agentic-autoscaler:dev --name agentic-autoscaler-dev
ok "Image built and loaded into kind"

# ── 4. Secrets ────────────────────────────────────────────────────────────────
step "4/7  Creating namespace and secrets"

kubectl create namespace "$OPERATOR_NS" --dry-run=client -o yaml | kubectl apply -f -

if [ -n "$ANTHROPIC_API_KEY" ]; then
    ok "ANTHROPIC_API_KEY found — AI reasoning enabled"
else
    warn "ANTHROPIC_API_KEY not set (shell env or .env) — using rule-based fallback"
    warn "Re-run with: ANTHROPIC_API_KEY=sk-ant-... bash hack/bootstrap.sh"
fi

# Never overwrite an existing real key with a placeholder.
ensure_secret ai-provider-secret API_KEY      "$ANTHROPIC_API_KEY"
ensure_secret grafana-api-secret GRAFANA_TOKEN "$GRAFANA_TOKEN"

# Annotate secrets with Helm ownership so helm upgrade --install can adopt them
for secret in ai-provider-secret grafana-api-secret; do
    kubectl annotate secret "$secret" \
        meta.helm.sh/release-name=agentic-autoscaler \
        meta.helm.sh/release-namespace="$OPERATOR_NS" \
        --namespace "$OPERATOR_NS" --overwrite
    kubectl label secret "$secret" \
        app.kubernetes.io/managed-by=Helm \
        --namespace "$OPERATOR_NS" --overwrite
done

ok "Secrets applied"

# ── 5. Deploy operator ────────────────────────────────────────────────────────
step "5/7  Deploying operator"

kubectl apply -f config/crd/bases/

helm dependency update deploy/helm/ > /dev/null 2>&1 || true

# ── AI provider selection ─────────────────────────────────────────────────
# AI_PROVIDER: anthropic (default) | ollama | openai. For ollama we point the
# operator at a host-native Ollama by default — it uses the host GPU
# automatically when present (an RTX card, etc.) and needs no in-cluster model
# pull. Override endpoint/model with OLLAMA_BASE_URL / OLLAMA_MODEL.
AI_PROVIDER="${AI_PROVIDER:-anthropic}"
EXTRA_SET=()

if [ "$AI_PROVIDER" = "ollama" ]; then
    OLLAMA_BASE_URL="${OLLAMA_BASE_URL:-http://host.docker.internal:11434}"
    OLLAMA_MODEL="${OLLAMA_MODEL:-qwen2.5:3b}"
    EXTRA_SET+=(--set "ollama.baseURL=$OLLAMA_BASE_URL")
    EXTRA_SET+=(--set "ollama.model=$OLLAMA_MODEL")
    EXTRA_SET+=(--set "ollama.enabled=false")
    if command -v nvidia-smi >/dev/null 2>&1; then
        ok "GPU detected (nvidia-smi) — host-native Ollama will use it automatically"
    else
        warn "No GPU detected — Ollama will run on CPU (small models only); for lower latency use AI_PROVIDER=anthropic"
    fi
    warn "Run Ollama on the host first:  ollama serve  &&  ollama pull $OLLAMA_MODEL"
    warn "Operator will reach Ollama at $OLLAMA_BASE_URL"
else
    [ -n "$ANTHROPIC_MODEL" ] && EXTRA_SET+=(--set "aiProvider.model=$ANTHROPIC_MODEL")
fi

if [ "$WITH_LOADGEN" = "1" ]; then
    EXTRA_SET+=(--set console.writeEnabled=true)
    EXTRA_SET+=(--set console.loadgenURL=http://loadgen.loadgen.svc.cluster.local:8080)
fi

helm upgrade --install agentic-autoscaler deploy/helm/ \
    --namespace "$OPERATOR_NS" \
    --set image.repository=agentic-autoscaler \
    --set image.tag=dev \
    --set image.pullPolicy=Never \
    --set "aiProvider.provider=$AI_PROVIDER" \
    --set aiProvider.secretRef=ai-provider-secret \
    --set grafana.url="http://kube-prometheus-stack-grafana.monitoring" \
    --set grafana.secretRef=grafana-api-secret \
    --set dryRun=true \
    --set networkPolicy.enabled=false \
    "${EXTRA_SET[@]}" \
    --timeout 5m

ok "Operator Helm release installed"

echo "  → Waiting up to 120s for operator rollout..."
if kubectl rollout status "deployment/$DEPLOY_NAME" \
        -n "$OPERATOR_NS" --timeout=120s 2>/dev/null; then
    ok "Operator pod ready"
else
    warn "Operator pod not ready — check logs with:"
    warn "  kubectl get pods -n $OPERATOR_NS"
    warn "  kubectl logs -n $OPERATOR_NS -l app.kubernetes.io/name=agentic-autoscaler"
    kubectl get pods -n "$OPERATOR_NS" 2>/dev/null || true
fi

ok "Operator deployed  (dry-run=true)"

# ── 6. Sample app ─────────────────────────────────────────────────────────────
step "6/7  Deploying sample payment-service"

kubectl apply -f config/samples/dev-payment-service.yaml
ok "payment-service deployed  (3 replicas + HPA)"

# ── 7. AgenticAutoscaler CR ───────────────────────────────────────────────────
step "7/7  Creating AgenticAutoscaler CR"

kubectl apply -f - <<EOF
apiVersion: scaling.autoscaler.io/v1alpha1
kind: AgenticAutoscaler
metadata:
  name: payment-service-autoscaler
  namespace: production
spec:
  targetDeployment: payment-service
  prometheusURL: "http://kube-prometheus-stack-prometheus.monitoring:9090"
  logSource:
    type: loki
    loki:
      url: "http://loki.monitoring:3100"
      query: '{namespace="production", app="payment-service"}'
  minReplicas: 3
  maxReplicas: 18
  cooldownSeconds: 300
  dryRun: true
  aiProvider:
    provider: anthropic
    secretRef: ai-provider-secret
  hpaCoexistence:
    mode: calibrated
    hpaName: payment-service-hpa
    hpaNamespace: production
  observability:
    grafanaURL: "http://kube-prometheus-stack-grafana.monitoring"
    secretRef: grafana-api-secret
EOF

ok "AgenticAutoscaler created  (dry-run=true)"

# ── 8. Demo extras (only with WITH_LOADGEN=1) ────────────────────────────────
if [ "$WITH_LOADGEN" = "1" ]; then
    step "8  Demo: load generator + live scaling"

    echo "  → Building loadgen image..."
    docker build -f Dockerfile.loadgen -t agentic-autoscaler-loadgen:dev .
    kind load docker-image agentic-autoscaler-loadgen:dev --name agentic-autoscaler-dev
    kubectl apply -f deploy/loadgen/loadgen.yaml
    kubectl -n loadgen rollout status deploy/loadgen --timeout=120s || \
        warn "loadgen not ready yet — check: kubectl -n loadgen get pods"
    ok "Load generator deployed"

    echo "  → Switching payment-service-autoscaler to LIVE (dryRun=false, cooldown=60s)..."
    kubectl -n production patch agenticautoscaler payment-service-autoscaler \
        --type merge -p '{"spec":{"dryRun":false,"cooldownSeconds":60}}'
    ok "Autoscaler is now LIVE — console write mode and Load tab are enabled"
fi

# ── Done ──────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  Bootstrap complete!${NC}"
echo ""
echo "  Open port-forwards in a NEW terminal tab:"
echo ""
echo -e "  ${CYAN}bash hack/port-forward.sh${NC}"
echo ""
echo "  Then open in your browser:"
echo "    Grafana   →  http://localhost:3000   (admin / admin)"
echo "    Console   →  http://localhost:8090"
echo "    Prometheus→  http://localhost:9090"
echo ""
if [ "$WITH_LOADGEN" = "1" ]; then
    echo "  Drive load (watch replicas climb), e.g.:"
    echo -e "  ${CYAN}make load-up RPS=300 ERRPCT=15${NC}    (or use the console Load tab)"
    echo -e "  ${CYAN}make load-down${NC}                    (stop → scales back down)"
    echo ""
else
    echo "  This is the BASE stack (dry-run, read-only console)."
    echo "  For load-driven LIVE scaling, re-run with:"
    echo -e "  ${CYAN}WITH_LOADGEN=1 bash hack/bootstrap.sh${NC}"
    echo ""
fi
echo "  Watch live operator decisions:"
echo -e "  ${CYAN}kubectl -n $OPERATOR_NS logs -l app.kubernetes.io/name=agentic-autoscaler -f${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
