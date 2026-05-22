#!/usr/bin/env bash
# Forward all dev-stack ports to localhost.
# Run this in a dedicated terminal — it stays open until you press Ctrl-C.
#
# Usage:
#   bash hack/port-forward.sh
set -euo pipefail

CYAN='\033[0;36m'; GREEN='\033[0;32m'; NC='\033[0m'

echo -e "${GREEN}Starting port-forwards — press Ctrl-C to stop all${NC}"
echo ""
echo -e "  ${CYAN}Grafana   ${NC}→  http://localhost:3000  (admin / admin)"
echo -e "  ${CYAN}Prometheus${NC}→  http://localhost:9090"
echo -e "  ${CYAN}Query UI  ${NC}→  http://localhost:8090"
echo ""

# Kill any existing forwards on these ports
for port in 3000 9090 8090; do
    pid=$(lsof -ti tcp:"$port" 2>/dev/null || true)
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
done

# Start all three in background, kill all when script exits
cleanup() {
    echo -e "\n${CYAN}Stopping port-forwards...${NC}"
    kill "${PF_GRAFANA_PID:-}" "${PF_PROM_PID:-}" "${PF_UI_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

kubectl -n monitoring port-forward \
    svc/kube-prometheus-stack-grafana 3000:80 &
PF_GRAFANA_PID=$!

kubectl -n monitoring port-forward \
    svc/kube-prometheus-stack-prometheus 9090:9090 &
PF_PROM_PID=$!

kubectl -n agentic-autoscaler-system port-forward \
    svc/agentic-autoscaler-agentic-autoscaler 8090:8090 &
PF_UI_PID=$!

echo -e "${GREEN}All forwards active. Open your browser.${NC}"
wait
