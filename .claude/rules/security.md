# Security rules

## Secrets — never in CRD spec
- API keys (Anthropic, OpenAI) must be referenced via `secretKeyRef` in the operator Deployment, not stored in AgenticAutoscaler spec
- Loki/Kafka credentials use the same pattern
- Never log secret values — redact before logging

## RBAC — minimum required permissions
The operator ServiceAccount must only have:
- `get/list/watch/update/patch` on `Deployments` in target namespaces
- `get/list/watch` on `HorizontalPodAutoscalers`
- `get/list/watch/create/update/patch` on `AgenticAutoscalers` and `AgenticAutoscalers/status`
- `get/list/watch/create/update/patch` on `ConfigMaps` (for decision log)
- No cluster-admin, no secrets read

## Network policy
The operator pod should only be allowed to:
- Reach Prometheus (port 9090) in the monitoring namespace
- Reach Loki (port 3100) in the monitoring namespace
- Reach the LLM API endpoint (external) or Ollama pod (internal)
- Reach the Kubernetes API server

## Image security
- Use distroless or scratch base image for the operator
- Pin image digest, not just tag, in production manifests
- Run as non-root user (runAsNonRoot: true, runAsUser: 65534)
