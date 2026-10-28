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
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

const (
	openaiDefaultEndpoint = "https://api.openai.com"
	openaiDefaultModel    = "gpt-4o"
	openaiMaxTokens       = 256
	openaiHTTPTimeout     = 10 * time.Second
	openaiMaxRetries      = 2
	openaiRetryBackoff    = 500 * time.Millisecond
)

// OpenAIAgent calls the OpenAI Chat Completions API to make scaling decisions.
// On any failure after retries it logs a warning and delegates to the
// rule-based fallback so the operator continues functioning without an LLM.
type OpenAIAgent struct {
	cfg      Config
	client   *http.Client
	fallback Agent
}

// NewOpenAIAgent returns an OpenAIAgent pointed at the OpenAI API.
// cfg.APIKey must be populated from the operator's mounted K8s Secret; it is
// never logged by this package. cfg.BaseURL, if non-empty, overrides the API
// base URL (tests, or an OpenAI-compatible gateway such as Azure OpenAI or vLLM).
func NewOpenAIAgent(cfg Config) Agent {
	return &OpenAIAgent{
		cfg:      cfg,
		client:   &http.Client{Timeout: openaiHTTPTimeout},
		fallback: NewRuleBasedAgent(cfg),
	}
}

// Decide calls the OpenAI API, parses the JSON decision, and returns a
// ScaleDecision. On any error it falls through to the rule-based fallback.
func (o *OpenAIAgent) Decide(ctx context.Context, signal fusion.FusedSignal) (ScaleDecision, error) {
	bodyBytes, err := o.buildRequestBody(signal)
	if err != nil {
		log.Printf("WARNING: openai: failed to build request body: %v; using rule-based fallback", err)
		return o.fallback.Decide(ctx, signal)
	}

	respBytes, err := o.callWithRetry(ctx, bodyBytes)
	if err != nil {
		log.Printf("WARNING: openai: API call failed: %v; using rule-based fallback", err)
		return o.fallback.Decide(ctx, signal)
	}

	decision, err := o.parseDecision(respBytes)
	if err != nil {
		log.Printf("WARNING: openai: response parse failed: %v; using rule-based fallback", err)
		return o.fallback.Decide(ctx, signal)
	}

	return decision, nil
}

// buildRequestBody marshals the Chat Completions request. response_format
// json_object makes the API reject non-JSON output server-side. Temperature is
// left at the provider default because newer reasoning models refuse any other
// value; the parser and bounds validation already absorb output variation.
func (o *OpenAIAgent) buildRequestBody(signal fusion.FusedSignal) ([]byte, error) {
	req := openaiRequest{
		Model:               o.modelName(),
		MaxCompletionTokens: openaiMaxTokens,
		ResponseFormat:      &openaiResponseFormat{Type: "json_object"},
		Messages: []openaiMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: BuildPrompt(o.cfg, signal)},
		},
	}
	return json.Marshal(req)
}

// callWithRetry POSTs to the OpenAI API with the shared cloud retry policy
// (2 retries, 500ms backoff on 429/5xx).
func (o *OpenAIAgent) callWithRetry(ctx context.Context, body []byte) ([]byte, error) {
	headers := map[string]string{
		"Authorization": "Bearer " + o.cfg.APIKey, // secret — never appears in logs
	}
	return cloudPostWithRetry(ctx, o.client, o.completionsEndpoint(), headers, body,
		openaiMaxRetries, openaiRetryBackoff)
}

// parseDecision extracts the AI's JSON from the first choice of the Chat
// Completions envelope, validates it, and maps it to a ScaleDecision.
func (o *OpenAIAgent) parseDecision(raw []byte) (ScaleDecision, error) {
	var envelope openaiResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ScaleDecision{}, fmt.Errorf("unmarshal openai envelope: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return ScaleDecision{}, fmt.Errorf("no choices in openai response")
	}
	choice := envelope.Choices[0]
	if choice.Message.Refusal != "" {
		return ScaleDecision{}, fmt.Errorf("model refused: %s", choice.Message.Refusal)
	}
	if choice.Message.Content == "" {
		return ScaleDecision{}, fmt.Errorf("empty message content (finish_reason %q)", choice.FinishReason)
	}

	var ai aiDecision
	if err := json.Unmarshal([]byte(extractJSONObject(choice.Message.Content)), &ai); err != nil {
		return ScaleDecision{}, fmt.Errorf("parse ai decision JSON %q: %w", choice.Message.Content, err)
	}

	if err := o.validateDecision(ai); err != nil {
		return ScaleDecision{}, fmt.Errorf("invalid ai decision: %w", err)
	}

	return ScaleDecision{
		Action:         ai.Action,
		TargetReplicas: ai.TargetReplicas,
		Confidence:     ai.Confidence,
		Reason:         ai.Reason,
		Timestamp:      nowMetav1(),
		Provider:       "openai",
	}, nil
}

// validateDecision rejects decisions with unknown actions, out-of-bounds
// replica counts, or confidence values outside [0, 1].
func (o *OpenAIAgent) validateDecision(d aiDecision) error {
	switch d.Action {
	case "scale-up", "scale-down", ActionHold:
	default:
		return fmt.Errorf("unknown action %q", d.Action)
	}
	if d.Action != ActionHold {
		if d.TargetReplicas < o.cfg.MinReplicas || d.TargetReplicas > o.cfg.MaxReplicas {
			return fmt.Errorf("targetReplicas %d out of bounds [%d, %d]",
				d.TargetReplicas, o.cfg.MinReplicas, o.cfg.MaxReplicas)
		}
	}
	if d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("confidence %.3f out of range [0.0, 1.0]", d.Confidence)
	}
	return nil
}

// completionsEndpoint returns the full Chat Completions URL. cfg.BaseURL, if
// set, replaces the production base so tests and compatible gateways work.
func (o *OpenAIAgent) completionsEndpoint() string {
	base := openaiDefaultEndpoint
	if o.cfg.BaseURL != "" {
		base = o.cfg.BaseURL
	}
	return base + "/v1/chat/completions"
}

func (o *OpenAIAgent) modelName() string {
	if o.cfg.Model != "" {
		return o.cfg.Model
	}
	return openaiDefaultModel
}

// ---- OpenAI API wire types -------------------------------------------------

type openaiRequest struct {
	Model               string                `json:"model"`
	MaxCompletionTokens int                   `json:"max_completion_tokens"`
	ResponseFormat      *openaiResponseFormat `json:"response_format,omitempty"`
	Messages            []openaiMessage       `json:"messages"`
}

type openaiResponseFormat struct {
	Type string `json:"type"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Refusal string `json:"refusal,omitempty"`
}

type openaiResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
}

type openaiChoice struct {
	Index        int           `json:"index"`
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}
