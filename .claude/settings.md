# Hook documentation

## PreToolUse hooks

### Branch protection
Blocks all file edits on `main`. Create a feature branch before making changes.
- Trigger: any `Edit` or `Write` tool call
- Exit code 2 = blocked with message

### pkg purity guard
Blocks edits to `pkg/signals/` or `pkg/fusion/` that introduce Kubernetes imports.
These two packages must remain pure Go with no `k8s.io` or `sigs.k8s.io` dependencies.
This makes them independently testable and reusable outside Kubernetes.

## PostToolUse hooks

### Auto gofmt
Runs `gofmt -w` on every `.go` file after editing. No manual formatting needed.

### api change reminder
When any file under `api/v1alpha1/` is edited, reminds Claude to run
`make generate && make manifests` to regenerate CRD YAML and deep-copy functions.

## UserPromptSubmit hooks

### Skill evaluation
Runs `.claude/hooks/skill-eval.sh` on every prompt. Analyzes keywords, file paths,
and intent to suggest which skill documents Claude should read before proceeding.
