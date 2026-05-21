#!/usr/bin/env bash
# Skill evaluation hook — runs on every UserPromptSubmit
# Delegates to skill-eval.js if node is available, otherwise does basic bash matching

PROMPT="${CLAUDE_USER_PROMPT:-}"
PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"
RULES_FILE="$PROJECT_DIR/.claude/hooks/skill-rules.json"

if command -v node &>/dev/null && [ -f "$PROJECT_DIR/.claude/hooks/skill-eval.js" ]; then
  node "$PROJECT_DIR/.claude/hooks/skill-eval.js" "$PROMPT" "$RULES_FILE"
  exit $?
fi

# Bash fallback — basic keyword matching
SUGGESTIONS=""

if echo "$PROMPT" | grep -qiE 'reconcil|operator|controller|crd|custom.resource'; then
  SUGGESTIONS="$SUGGESTIONS\n- operator-patterns: .claude/skills/operator-patterns/SKILL.md"
fi
if echo "$PROMPT" | grep -qiE 'prometheus|metric|log|signal|snapshot|loki|kafka|fluentd'; then
  SUGGESTIONS="$SUGGESTIONS\n- signal-fusion: .claude/skills/signal-fusion/SKILL.md"
fi
if echo "$PROMPT" | grep -qiE 'reason|agent|llm|ai|decision|scale.decis|anthropic|openai|ollama'; then
  SUGGESTIONS="$SUGGESTIONS\n- ai-reasoning: .claude/skills/ai-reasoning/SKILL.md"
fi
if echo "$PROMPT" | grep -qiE 'hpa|horizontal.pod|cooldown|bound|min.replica|max.replica|conflict'; then
  SUGGESTIONS="$SUGGESTIONS\n- hpa-integration: .claude/skills/hpa-integration/SKILL.md"
fi
if echo "$PROMPT" | grep -qiE 'deploy|patch|replica|keda|scale.object|k8s.api|client.go'; then
  SUGGESTIONS="$SUGGESTIONS\n- kubernetes-api: .claude/skills/kubernetes-api/SKILL.md"
fi
if echo "$PROMPT" | grep -qiE 'grafana|annotation|observ|explain|audit|dashboard'; then
  SUGGESTIONS="$SUGGESTIONS\n- observability: .claude/skills/observability/SKILL.md"
fi

if [ -n "$SUGGESTIONS" ]; then
  echo "{\"feedback\": \"Suggested skills for this task:\\n$SUGGESTIONS\"}" >&2
fi
