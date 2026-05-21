---
description: Summarize the dry-run decision log for a deployment — what would have happened
allowed-tools: Read, Bash(kubectl:*)
---

# Dry-run report for: $ARGUMENTS

Deployment: $1
Namespace: $2 (default: production)

## Steps

1. Run: kubectl get agenticautoscaler -n ${2:-production} $1 -o yaml
2. Read the status.lastDecisionReason and status.lastScaleTime fields
3. Fetch the decision audit ConfigMap:
   kubectl get configmap decision-audit-log -n agentic-autoscaler-system -o jsonpath='{.data.log}' | jq '.[] | select(.deployment == "$1")'
4. Summarize:
   - How many scale-up decisions in the last 24h?
   - How many would have changed replica count by >50%?
   - What log patterns triggered the most decisions?
   - Are there any decisions where AI confidence was < 0.5?
5. Recommend: based on the dry-run data, is this deployment ready to enable live mode?

## Usage
/dry-run-report payment-service production
