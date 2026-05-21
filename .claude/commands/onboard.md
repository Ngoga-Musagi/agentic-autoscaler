---
description: Deep exploration of a task or feature before starting implementation
allowed-tools: Read, Grep, Glob, Bash(git:*), Bash(go:*)
---

# Onboard to task: $ARGUMENTS

Before writing any code, fully understand what is being asked.

## Steps

1. Read CLAUDE.md for project context
2. Read the relevant skill documents based on the task keywords:
   - operator/reconcile/crd → .claude/skills/operator-patterns/SKILL.md
   - signal/metric/log/fusion → .claude/skills/signal-fusion/SKILL.md
   - reasoning/ai/decision → .claude/skills/ai-reasoning/SKILL.md
   - hpa/coexistence/bounds → .claude/skills/hpa-integration/SKILL.md
   - scale/patch/keda → .claude/skills/kubernetes-api/SKILL.md
   - grafana/annotation/audit → .claude/skills/observability/SKILL.md
3. Read the relevant source files for current state
4. Identify:
   - Which packages will be touched?
   - What interfaces must be preserved?
   - What tests must pass?
   - Are there policy constraints? (.claude/rules/)
5. Produce a short plan (5–10 bullet points) before writing any code
6. Ask for confirmation before proceeding

## Usage
/onboard Add OOMKilled pattern to signal fusion and connect it to the reasoning layer
