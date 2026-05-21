---
description: Scaffold a new AgenticAutoscaler manifest alongside an existing Deployment. Usage: /new-autoscaler <namespace>/<deployment-name>
allowed-tools: Read, Bash(kubectl get:*), Bash(kubectl describe:*)
---

Your task is to create a production-ready AgenticAutoscaler manifest for: $ARGUMENTS

## Steps

1. Parse the argument as <namespace>/<deployment-name>

2. Read the existing deployment:
   !`kubectl get deployment $2 -n $1 -o yaml 2>/dev/null || echo "NOT FOUND"`

3. Check for an existing HPA:
   !`kubectl get hpa $2 -n $1 -o yaml 2>/dev/null || echo "NO HPA"`

4. Generate the AgenticAutoscaler manifest following these rules:
   - Set targetDeployment to the deployment name
   - Set dryRun: true (always start dry-run)
   - If HPA exists: set hpaCoexistence.enabled: true and calibrate bounds INSIDE HPA range
   - If no HPA: set hpaCoexistence.enabled: false, choose sensible min/max based on current replicas
   - Set reasoningBackend.provider: cloud (with a TODO comment for switching to ollama)
   - Use placeholder values for prometheusURL and logSource that match the project's defaults
   - Include the three default log patterns: connection-pool-exhausted, upstream-timeout, oom-killed

5. Output the complete YAML and tell the user:
   - Where to save the file (next to the Deployment)
   - What to do before setting dryRun: false
   - How to monitor the decision log in Grafana
