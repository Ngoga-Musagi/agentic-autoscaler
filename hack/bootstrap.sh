#!/usr/bin/env bash
# Bootstrap the full agentic-autoscaler dev stack.
# Works in Git Bash (Windows) and WSL2 — no 'make' required.
# Auto-installs kind and kubectl if missing.
#
# Usage:
#   bash hack/bootstrap.sh
#
# Optional env vars:
#   ANTHROPIC_API_KEY=sk-ant-...   enables AI reasoning (falls back to rules if unset)
#   GRAFANA_TOKEN=...              enables Grafana annotation push (optional)
set -euo pipefail

CYAN='\033[0;36m'; GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
step() { echo -e "\n${CYAN}━━ $* ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"; }
ok()   { echo -e "${GREEN}  ✓ $*${NC}"; }
warn() { echo -e "${YELLOW}  ⚠ $*${NC}"; }
die()  { echo -e "${RED}  ✗ $*${NC}"; exit 1; }

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

echo "  → loki-stack..."
helm upgrade --install loki grafana/loki-stack \
    --namespace monitoring \
    --set promtail.enabled=true \
    --set loki.persistence.enabled=false \
    --wait --timeout 4m
ok "Loki ready"

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

kubectl create namespace agentic-autoscaler-system --dry-run=client -o yaml | kubectl apply -f -

if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    ok "ANTHROPIC_API_KEY found — AI reasoning enabled"
else
    warn "ANTHROPIC_API_KEY not set — using rule-based fallback"
    warn "Re-run with: ANTHROPIC_API_KEY=sk-ant-... bash hack/bootstrap.sh"
fi

kubectl create secret generic ai-provider-secret \
    --from-literal=API_KEY="${ANTHROPIC_API_KEY:-not-configured}" \
    --namespace agentic-autoscaler-system \
    --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic grafana-api-secret \
    --from-literal=GRAFANA_TOKEN="${GRAFANA_TOKEN:-not-configured}" \
    --namespace agentic-autoscaler-system \
    --dry-run=client -o yaml | kubectl apply -f -

# Annotate secrets with Helm ownership so helm upgrade --install can adopt them
for secret in ai-provider-secret grafana-api-secret; do
    kubectl annotate secret "$secret" \
        meta.helm.sh/release-name=agentic-autoscaler \
        meta.helm.sh/release-namespace=agentic-autoscaler-system \
        --namespace agentic-autoscaler-system --overwrite
    kubectl label secret "$secret" \
        app.kubernetes.io/managed-by=Helm \
        --namespace agentic-autoscaler-system --overwrite
done

ok "Secrets applied"

# ── 5. Deploy operator ────────────────────────────────────────────────────────
step "5/7  Deploying operator"

kubectl apply -f config/crd/bases/

helm dependency update deploy/helm/ > /dev/null 2>&1 || true

helm upgrade --install agentic-autoscaler deploy/helm/ \
    --namespace agentic-autoscaler-system \
    --set image.repository=agentic-autoscaler \
    --set image.tag=dev \
    --set image.pullPolicy=Never \
    --set aiProvider.provider=anthropic \
    --set aiProvider.secretRef=ai-provider-secret \
    --set grafana.url="http://kube-prometheus-stack-grafana.monitoring" \
    --set grafana.secretRef=grafana-api-secret \
    --set dryRun=true \
    --set networkPolicy.enabled=false \
    --timeout 5m

ok "Operator Helm release installed"

echo "  → Waiting up to 120s for operator rollout..."
if kubectl rollout status deployment/agentic-autoscaler \
        -n agentic-autoscaler-system --timeout=120s 2>/dev/null; then
    ok "Operator pod ready"
else
    warn "Operator pod not ready — check logs with:"
    warn "  kubectl get pods -n agentic-autoscaler-system"
    warn "  kubectl logs -n agentic-autoscaler-system -l app.kubernetes.io/name=agentic-autoscaler"
    kubectl get pods -n agentic-autoscaler-system 2>/dev/null || true
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
echo "    Query UI  →  http://localhost:8090"
echo "    Prometheus→  http://localhost:9090"
echo ""
echo "  Watch live operator decisions:"
echo -e "  ${CYAN}kubectl -n agentic-autoscaler-system logs -l app.kubernetes.io/name=agentic-autoscaler -f${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
