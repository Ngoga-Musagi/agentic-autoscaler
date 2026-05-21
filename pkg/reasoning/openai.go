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

package reasoning

import (
	"context"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

// OpenAIAgent calls the OpenAI Chat Completions API to make scaling decisions.
// It falls back to the rule-based agent when the API is unavailable.
//
// Phase 4 placeholder: HTTP client, prompt construction, retry, and fallback
// are not yet wired up. All calls delegate to the rule-based agent until then.
type OpenAIAgent struct {
	cfg      Config
	fallback Agent
}

// NewOpenAIAgent returns an OpenAIAgent pointed at the OpenAI API.
// cfg.APIKey must be populated from the operator's mounted K8s Secret.
func NewOpenAIAgent(cfg Config) Agent {
	return &OpenAIAgent{
		cfg:      cfg,
		fallback: NewRuleBasedAgent(cfg),
	}
}

// Decide queries the OpenAI API and parses its JSON response into a ScaleDecision.
// TODO(phase-4): implement HTTP call, prompt rendering, retry on 429/5xx, and JSON parse.
func (o *OpenAIAgent) Decide(ctx context.Context, signal fusion.FusedSignal) (ScaleDecision, error) {
	d, err := o.fallback.Decide(ctx, signal)
	if err != nil {
		return d, err
	}
	d.Provider = "openai"
	return d, nil
}
