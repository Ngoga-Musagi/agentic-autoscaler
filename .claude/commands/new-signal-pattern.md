---
description: Add a new log pattern to the signal fusion engine
allowed-tools: Read, Grep, Glob, Edit, Bash(go:*)
---

# Add log pattern: $ARGUMENTS

You are adding a new named log pattern to `pkg/fusion/patterns.go`.

## Steps

1. Read `pkg/fusion/patterns.go` to understand the existing pattern format
2. Read `.claude/skills/signal-fusion/SKILL.md` for the Pattern struct definition
3. Add the new pattern with:
   - Name: $1 (slug format, e.g. "rate-limit-exceeded")
   - Regex: $2 (the pattern to match in log lines)
   - Severity: $3 ("warning" or "critical")
   - Score: a float between 0.0 and 1.0 based on severity
4. Add a test case in `pkg/fusion/patterns_test.go` with a sample log line that should match
5. Run `go test ./pkg/fusion/...` to confirm the test passes
6. Run `go test ./...` to confirm no regressions

## Usage
/new-signal-pattern rate-limit-exceeded "rate limit exceeded|429 Too Many Requests" warning
