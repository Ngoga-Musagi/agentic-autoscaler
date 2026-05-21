/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package reasoning selects and executes the scaling decision engine.
// It supports cloud (Anthropic, OpenAI) and self-hosted (Ollama) AI providers,
// with a deterministic rule-based fallback that requires no external calls.
//
// The controller must always call NewAgent — never reach into a provider
// package directly.
package reasoning

import (
	"context"
	"math"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

// Agent is the single interface all reasoning backends implement.
// The controller calls Decide once per reconcile cycle and acts on the result.
type Agent interface {
	Decide(ctx context.Context, signal fusion.FusedSignal) (ScaleDecision, error)
}

// ScaleDecision is the output of the reasoning engine for one reconcile cycle.
type ScaleDecision struct {
	// Action is "scale-up", "scale-down", or "hold".
	Action string

	// TargetReplicas is the desired replica count to apply.
	// For "hold" the value is not acted upon by the scaler.
	TargetReplicas int32

	// Confidence is the agent's certainty in this decision (0.0–1.0).
	Confidence float64

	// Reason is a natural-language explanation written for on-call SREs.
	Reason string

	// Timestamp is when the decision was produced.
	Timestamp *metav1.Time

	// Provider identifies which reasoning backend produced this decision:
	// "anthropic", "openai", "ollama", or "rule-based".
	Provider string

	// OldReplicas is the replica count of the target Deployment before this
	// decision was applied. Set by the controller from the live Deployment.
	OldReplicas int32

	// PatternsMatched lists the log pattern names that fired during signal
	// fusion and influenced this decision.
	PatternsMatched []string
}

// Config holds every value an Agent implementation needs at construction time.
// CurrentReplicas, MinReplicas, and MaxReplicas must be refreshed from the
// live Deployment and CR spec on every reconcile before calling NewAgent.
type Config struct {
	// Provider selects the backend: "anthropic", "openai", "ollama", or "" (rule-based).
	Provider string

	// APIKey is the bearer token for cloud providers, sourced from a K8s Secret.
	// Never log this value — it is a secret.
	APIKey string

	// Model is the model identifier passed to the provider API.
	Model string

	// BaseURL overrides the provider's default endpoint (used for Ollama and tests).
	BaseURL string

	// TargetDeployment is the name of the deployment being managed, included in
	// AI prompts so the model can produce deployment-specific reasoning.
	TargetDeployment string

	// CurrentReplicas is the live replica count of the target Deployment.
	CurrentReplicas int32

	// MinReplicas is the lower bound from the AgenticAutoscaler spec.
	MinReplicas int32

	// MaxReplicas is the upper bound from the AgenticAutoscaler spec.
	MaxReplicas int32
}

// NewAgent returns the Agent implementation selected by cfg.Provider.
// An unknown or empty Provider silently falls back to the rule-based agent
// so that the operator degrades gracefully on misconfiguration.
func NewAgent(cfg Config) Agent {
	switch cfg.Provider {
	case "anthropic":
		return NewAnthropicAgent(cfg)
	case "openai":
		return NewOpenAIAgent(cfg)
	case "ollama":
		return NewOllamaAgent(cfg)
	default:
		return NewRuleBasedAgent(cfg)
	}
}

// NewAgentFromEnv overlays provider credentials from environment variables onto
// cfg (which supplies the Kubernetes-specific fields CurrentReplicas, MinReplicas,
// MaxReplicas, and TargetDeployment from the CRD spec), then calls NewAgent.
//
// Environment variables read:
//
//	AI_PROVIDER          — "anthropic", "openai", "ollama", or "rules" (default: "rules")
//	ANTHROPIC_API_KEY    — bearer token for the Anthropic API
//	ANTHROPIC_MODEL      — model identifier (e.g. "claude-sonnet-4-20250514")
//	OPENAI_API_KEY       — bearer token for the OpenAI API
//	OPENAI_MODEL         — model identifier (e.g. "gpt-4o")
//	OLLAMA_BASE_URL      — in-cluster Ollama service URL
//	OLLAMA_MODEL         — model identifier (e.g. "llama3:8b")
func NewAgentFromEnv(cfg Config) Agent {
	cfg.Provider = envOrDefault("AI_PROVIDER", "rules")
	switch cfg.Provider {
	case "anthropic":
		cfg.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		cfg.Model = os.Getenv("ANTHROPIC_MODEL")
	case "openai":
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
		cfg.Model = os.Getenv("OPENAI_MODEL")
	case "ollama":
		cfg.BaseURL = os.Getenv("OLLAMA_BASE_URL")
		cfg.Model = os.Getenv("OLLAMA_MODEL")
	}
	return NewAgent(cfg)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---- package-level helpers used by all agent implementations ---------------

// scaleUpByFraction computes current + ⌈current × fraction⌉, clamped to max.
// At least one replica is always added to avoid a no-op scale-up.
func scaleUpByFraction(current int32, fraction float64, max int32) int32 {
	add := int32(math.Ceil(float64(current) * fraction))
	if add < 1 {
		add = 1
	}
	if target := current + add; target < max {
		return target
	}
	return max
}

// scaleDownToBaseline returns MinReplicas, the "baseline" replica count that
// the operator considers healthy when all signals have been green.
func scaleDownToBaseline(min int32) int32 {
	return min
}

// hold wraps HoldDecision to match the (ScaleDecision, error) return shape
// expected inside agent Decide methods.
func hold(reason string) (ScaleDecision, error) {
	return HoldDecision(reason), nil
}

// nowMetav1 returns the current wall-clock time as *metav1.Time.
func nowMetav1() *metav1.Time {
	t := metav1.NewTime(time.Now())
	return &t
}
