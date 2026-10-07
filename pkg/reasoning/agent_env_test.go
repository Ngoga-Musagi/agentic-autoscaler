package reasoning

import (
	"testing"
)

// baseK8sCfg carries the Kubernetes-sourced fields that the controller would
// populate from the CRD spec. NewAgentFromEnv overlays provider credentials
// on top of these, so tests set them to recognisable values.
var baseK8sCfg = Config{
	TargetDeployment: "payment-service",
	CurrentReplicas:  4,
	MinReplicas:      2,
	MaxReplicas:      20,
}

func TestNewAgentFromEnv_DefaultIsRuleBased(t *testing.T) {
	// No AI_PROVIDER set → safe default.
	t.Setenv("AI_PROVIDER", "")

	got := NewAgentFromEnv(baseK8sCfg)
	if _, ok := got.(*RuleBasedAgent); !ok {
		t.Errorf("want *RuleBasedAgent, got %T", got)
	}
}

func TestNewAgentFromEnv_ExplicitRules(t *testing.T) {
	t.Setenv("AI_PROVIDER", "rules")

	got := NewAgentFromEnv(baseK8sCfg)
	if _, ok := got.(*RuleBasedAgent); !ok {
		t.Errorf("want *RuleBasedAgent, got %T", got)
	}
}

func TestNewAgentFromEnv_Anthropic_TypeAndCredentials(t *testing.T) {
	t.Setenv("AI_PROVIDER", "ANTHROPIC_API_KEY")
	t.Setenv("AI_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
	t.Setenv("ANTHROPIC_MODEL", "claude-sonnet-5")

	got := NewAgentFromEnv(baseK8sCfg)
	a, ok := got.(*AnthropicAgent)
	if !ok {
		t.Fatalf("want *AnthropicAgent, got %T", got)
	}
	if a.cfg.APIKey != "sk-ant-test-key" {
		t.Errorf("APIKey: want %q, got %q", "sk-ant-test-key", a.cfg.APIKey)
	}
	if a.cfg.Model != "claude-sonnet-5" {
		t.Errorf("Model: want %q, got %q", "claude-sonnet-5", a.cfg.Model)
	}
	if a.cfg.TargetDeployment != baseK8sCfg.TargetDeployment {
		t.Errorf("TargetDeployment: want %q, got %q", baseK8sCfg.TargetDeployment, a.cfg.TargetDeployment)
	}
}

func TestNewAgentFromEnv_OpenAI_TypeAndCredentials(t *testing.T) {
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-openai-test-key")
	t.Setenv("OPENAI_MODEL", "gpt-4o")

	got := NewAgentFromEnv(baseK8sCfg)
	a, ok := got.(*OpenAIAgent)
	if !ok {
		t.Fatalf("want *OpenAIAgent, got %T", got)
	}
	if a.cfg.APIKey != "sk-openai-test-key" {
		t.Errorf("APIKey: want %q, got %q", "sk-openai-test-key", a.cfg.APIKey)
	}
	if a.cfg.Model != "gpt-4o" {
		t.Errorf("Model: want %q, got %q", "gpt-4o", a.cfg.Model)
	}
}

func TestNewAgentFromEnv_Ollama_TypeAndEndpoint(t *testing.T) {
	t.Setenv("AI_PROVIDER", "ollama")
	t.Setenv("OLLAMA_BASE_URL", "http://ollama.ollama.svc.cluster.local:11434")
	t.Setenv("OLLAMA_MODEL", "llama3:8b")

	got := NewAgentFromEnv(baseK8sCfg)
	a, ok := got.(*OllamaAgent)
	if !ok {
		t.Fatalf("want *OllamaAgent, got %T", got)
	}
	if a.cfg.BaseURL != "http://ollama.ollama.svc.cluster.local:11434" {
		t.Errorf("BaseURL: want %q, got %q", "http://ollama.ollama.svc.cluster.local:11434", a.cfg.BaseURL)
	}
	if a.cfg.Model != "llama3:8b" {
		t.Errorf("Model: want %q, got %q", "llama3:8b", a.cfg.Model)
	}
}

func TestNewAgentFromEnv_UnknownProvider_FallsBackToRuleBased(t *testing.T) {
	t.Setenv("AI_PROVIDER", "vertex-ai")

	got := NewAgentFromEnv(baseK8sCfg)
	if _, ok := got.(*RuleBasedAgent); !ok {
		t.Errorf("unknown provider should fall back to *RuleBasedAgent, got %T", got)
	}
}

func TestNewAgentFromEnv_K8sFieldsPreserved(t *testing.T) {
	// Verify that env var reading never clobbers the Kubernetes-sourced fields.
	t.Setenv("AI_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "key")

	got := NewAgentFromEnv(baseK8sCfg)
	a := got.(*AnthropicAgent)
	if a.cfg.MinReplicas != baseK8sCfg.MinReplicas {
		t.Errorf("MinReplicas: want %d, got %d", baseK8sCfg.MinReplicas, a.cfg.MinReplicas)
	}
	if a.cfg.MaxReplicas != baseK8sCfg.MaxReplicas {
		t.Errorf("MaxReplicas: want %d, got %d", baseK8sCfg.MaxReplicas, a.cfg.MaxReplicas)
	}
	if a.cfg.CurrentReplicas != baseK8sCfg.CurrentReplicas {
		t.Errorf("CurrentReplicas: want %d, got %d", baseK8sCfg.CurrentReplicas, a.cfg.CurrentReplicas)
	}
}

func TestNewAgentFromEnv_MissingModelEnvVar_UsesDefault(t *testing.T) {
	// When ANTHROPIC_MODEL is not set, the AnthropicAgent's modelName() should
	// return anthropicDefaultModel rather than an empty string.
	t.Setenv("AI_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("ANTHROPIC_MODEL", "") // explicitly empty

	got := NewAgentFromEnv(baseK8sCfg)
	a := got.(*AnthropicAgent)
	if a.modelName() == "" {
		t.Error("modelName() should not be empty when ANTHROPIC_MODEL is unset")
	}
	if a.modelName() != anthropicDefaultModel {
		t.Errorf("modelName(): want %q, got %q", anthropicDefaultModel, a.modelName())
	}
}
