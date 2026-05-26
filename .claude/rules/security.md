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

## Web console (management API on :8090)
The in-process console (`pkg/queryapi`) serves a read-only decision explorer plus
management endpoints that can create/edit/delete `AgenticAutoscaler` CRs.
- **Mutating endpoints are off by default.** `POST/PATCH/DELETE /api/autoscalers`
  and the `/api/load/*` proxy return `403` unless `CONSOLE_WRITE_ENABLED=true`
  (Helm: `console.writeEnabled`). Read endpoints are always available.
- When write mode is on, require a bearer token (`CONSOLE_AUTH_TOKEN`, Helm:
  `console.authSecretRef`). The token is compared in constant time and never
  logged. The chart never creates this Secret — manage it out-of-band.
- The console writes through the operator ServiceAccount, so it can only do what
  RBAC already allows (no new permissions). It must never read Secrets.
- `:8090` has no transport encryption — do not expose it outside the cluster.
  Keep access behind `kubectl port-forward`, an authenticated ingress, or a
  NetworkPolicy. Enabling write mode widens the blast radius to anyone who can
  reach the port with the token.

## Load generator (demo/testing only)
- `cmd/loadgen` + `deploy/loadgen/` is a traffic driver for demos. It is never
  installed by the default bootstrap and must not run in production.
- It runs distroless, non-root, read-only-root-fs like the operator.
- Point it only at services you are authorised to load-test.
