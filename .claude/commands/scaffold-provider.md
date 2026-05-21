---
description: Scaffold a new AI provider implementation in pkg/reasoning
allowed-tools: Read, Grep, Glob, Edit, Bash(go:*)
---

# Scaffold AI provider: $ARGUMENTS

Provider name: $1

## Steps

1. Read `pkg/reasoning/agent.go` to understand the Agent interface and Config struct
2. Read `.claude/skills/ai-reasoning/SKILL.md` for the provider pattern
3. Read an existing provider (e.g. `pkg/reasoning/anthropic.go`) for reference
4. Create `pkg/reasoning/$1.go` implementing the Agent interface:
   - Constructor: `New$1Agent(cfg Config) Agent`
   - Decide method with appropriate timeout
   - On failure: fall through to `RuleBasedAgent.Decide()`
5. Add `case "$1": return New$1Agent(cfg)` to `NewAgent()` in agent.go
6. Add required env vars to `.claude/rules/ai-provider.md`
7. Add env var documentation to `deploy/helm/templates/secret.yaml`
8. Write a test in `pkg/reasoning/${1}_test.go` using httptest.NewServer to stub the API
9. Run `go test ./pkg/reasoning/...`

## Usage
/scaffold-provider ollama
