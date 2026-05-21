---
description: Review recent agentic autoscaler decisions for a deployment. Usage: /check-decisions <namespace>/<deployment-name>
allowed-tools: Bash(kubectl get:*), Bash(kubectl describe:*)
---

Your task is to review recent scaling decisions for: $ARGUMENTS

## Steps

1. Parse <namespace>/<deployment-name> from the argument

2. Get the AgenticAutoscaler status:
   !`kubectl get agenticautoscaler $2 -n $1 -o yaml`

3. Get the decision log ConfigMap:
   !`kubectl get configmap $2-decisions -n $1 -o jsonpath='{.data}' 2>/dev/null || echo "No decision log found"`

4. Get current deployment state:
   !`kubectl get deployment $2 -n $1 -o jsonpath='{.spec.replicas} replicas'`

5. Get HPA state if present:
   !`kubectl get hpa $2 -n $1 2>/dev/null || echo "No HPA"`

6. Summarise:
   - Last N decisions with timestamp, direction, confidence, explanation, and source (rule-based/cloud-ai/ollama)
   - Whether any decisions were blocked by cooldown or policy bounds
   - Whether dryRun is still active
   - Recommendation: safe to disable dryRun? (only if 7+ days of clean decisions)
