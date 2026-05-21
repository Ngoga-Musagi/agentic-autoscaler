---
description: Switch an AgenticAutoscaler between cloud AI and Ollama backends. Usage: /switch-backend <namespace>/<name> <cloud|ollama>
allowed-tools: Read, Bash(kubectl get:*), Bash(kubectl patch:*)
---

Your task is to switch the reasoning backend for: $1 to: $2

## Steps

1. Validate argument 2 is either "cloud" or "ollama"

2. Get current spec:
   !`kubectl get agenticautoscaler $(echo $1 | cut -d/ -f2) -n $(echo $1 | cut -d/ -f1) -o yaml`

3. If switching to "ollama":
   - Check if Ollama is deployed: !`kubectl get deployment ollama -n agentic-autoscaler-system 2>/dev/null`
   - If not deployed, output the Ollama deployment YAML and tell the user to apply it first
   - Show the patch command to set provider: ollama and ollamaURL

4. If switching to "cloud":
   - Check the ANTHROPIC_API_KEY secret exists: !`kubectl get secret anthropic-api-key -n $(echo $1 | cut -d/ -f1) 2>/dev/null || echo "MISSING"`
   - If missing, explain how to create it
   - Show the patch command to set provider: cloud and model

5. Provide the exact kubectl patch command to apply the change without restarting the operator
