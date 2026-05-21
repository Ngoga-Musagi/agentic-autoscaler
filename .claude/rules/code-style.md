# Code style

## Go conventions
- Run `gofmt` before every commit — the PostToolUse hook does this automatically on file save
- Use `golangci-lint run ./...` for linting (config in `.golangci.yml`)
- Error strings must be lowercase and not end with punctuation: `"failed to query prometheus"` not `"Failed to query Prometheus."`
- Wrap errors with context: `fmt.Errorf("reconcile payment-service: %w", err)`
- Use `context.Context` as the first parameter of every function that does I/O

## Naming
- Kubernetes resource types: `PascalCase` (e.g. `AgenticAutoscaler`)
- Interface names: single method → name the interface after the method (e.g. `Decider`); multi-method → noun (e.g. `Agent`)
- Test files: `_test.go` suffix, same package as the code under test

## Package organisation
- One responsibility per package — never put signal collection and fusion in the same package
- No circular imports — dependency direction: `controller → reasoning → fusion → signals`
- No `utils` or `helpers` packages

## Comments
- All exported types and functions must have a doc comment
- CRD spec fields with constraints must have `// +kubebuilder:validation:` markers
- Comments explain *why*, not *what*

## Error handling
- Never ignore errors with `_` except `defer f.Close()` patterns
- Never use `panic` in production code paths
- Use `apierrors.IsNotFound(err)` to check missing resources
