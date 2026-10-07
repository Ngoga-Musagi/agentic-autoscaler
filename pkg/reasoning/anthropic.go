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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

const (
	anthropicDefaultEndpoint = "https://api.anthropic.com"
	anthropicAPIVersion      = "2023-06-01"
	anthropicDefaultModel    = "claude-sonnet-5"
	anthropicMaxTokens       = 256
	anthropicHTTPTimeout     = 10 * time.Second
	anthropicMaxRetries      = 2
	anthropicRetryBackoff    = 500 * time.Millisecond
)

// AnthropicAgent calls the Anthropic Messages API to make scaling decisions.
// On any failure after retries it logs a warning and delegates to the
// rule-based fallback so the operator continues functioning without an LLM.
type AnthropicAgent struct {
	cfg      Config
	client   *http.Client
	fallback Agent
}

// NewAnthropicAgent returns an AnthropicAgent.
// cfg.APIKey must be populated from the operator's K8s Secret mount — it is
// never logged by this package (security.md rule: never log secret values).
// cfg.BaseURL, if non-empty, overrides the Anthropic API base URL; this is
// used in tests to point the agent at an httptest server.
func NewAnthropicAgent(cfg Config) Agent {
	return &AnthropicAgent{
		cfg:      cfg,
		client:   &http.Client{Timeout: anthropicHTTPTimeout},
		fallback: NewRuleBasedAgent(cfg),
	}
}

// Decide calls the Anthropic API, parses the JSON decision, and returns a
// ScaleDecision. On any error it falls through to the rule-based fallback.
func (a *AnthropicAgent) Decide(ctx context.Context, signal fusion.FusedSignal) (ScaleDecision, error) {
	bodyBytes, err := a.buildRequestBody(signal)
	if err != nil {
		log.Printf("WARNING: anthropic: failed to build request body: %v; using rule-based fallback", err)
		return a.fallback.Decide(ctx, signal)
	}

	respBytes, err := a.callWithRetry(ctx, bodyBytes)
	if err != nil {
		log.Printf("WARNING: anthropic: API call failed: %v; using rule-based fallback", err)
		return a.fallback.Decide(ctx, signal)
	}

	decision, err := a.parseDecision(respBytes)
	if err != nil {
		log.Printf("WARNING: anthropic: response parse failed: %v; using rule-based fallback", err)
		return a.fallback.Decide(ctx, signal)
	}

	return decision, nil
}

// buildRequestBody marshals the Anthropic request envelope.
func (a *AnthropicAgent) buildRequestBody(signal fusion.FusedSignal) ([]byte, error) {
	req := anthropicRequest{
		Model:     a.modelName(),
		MaxTokens: anthropicMaxTokens,
		System:    SystemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: BuildPrompt(a.cfg, signal)},
		},
	}
	return json.Marshal(req)
}

// callWithRetry POSTs to the Anthropic API, retrying on 429 or 5xx up to
// anthropicMaxRetries times with a fixed backoff. Returns the raw response body
// on the first success. Context cancellation stops retries immediately.
func (a *AnthropicAgent) callWithRetry(ctx context.Context, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= anthropicMaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
			case <-time.After(anthropicRetryBackoff):
			}
		}

		respBody, status, err := a.doRequest(ctx, body)
		if err != nil {
			lastErr = fmt.Errorf("attempt %d: %w", attempt+1, err)
			continue // network/context error — retry
		}
		if status == http.StatusOK {
			return respBody, nil
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = fmt.Errorf("attempt %d: http %d", attempt+1, status)
			continue // retryable server error
		}
		// Non-retryable client error (4xx other than 429).
		return nil, fmt.Errorf("non-retryable http %d: %s", status, respBody)
	}
	return nil, fmt.Errorf("all %d attempts failed: %w", anthropicMaxRetries+1, lastErr)
}

// doRequest performs a single POST and returns (body, statusCode, error).
// The request body is re-created from body bytes on every call so retries
// are safe even after the first attempt has consumed the reader.
func (a *AnthropicAgent) doRequest(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, a.messagesEndpoint(), bytes.NewReader(body),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.cfg.APIKey) // secret — never appears in logs
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

// parseDecision extracts the AI's JSON from the Anthropic response envelope,
// validates it, and maps it to a ScaleDecision.
func (a *AnthropicAgent) parseDecision(raw []byte) (ScaleDecision, error) {
	var envelope anthropicResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ScaleDecision{}, fmt.Errorf("unmarshal anthropic envelope: %w", err)
	}

	text := ""
	for _, block := range envelope.Content {
		if block.Type == "text" {
			text = block.Text
			break
		}
	}
	if text == "" {
		return ScaleDecision{}, fmt.Errorf("no text content block in anthropic response")
	}

	var ai aiDecision
	if err := json.Unmarshal([]byte(text), &ai); err != nil {
		return ScaleDecision{}, fmt.Errorf("parse ai decision JSON %q: %w", text, err)
	}

	if err := a.validateDecision(ai); err != nil {
		return ScaleDecision{}, fmt.Errorf("invalid ai decision: %w", err)
	}

	return ScaleDecision{
		Action:         ai.Action,
		TargetReplicas: ai.TargetReplicas,
		Confidence:     ai.Confidence,
		Reason:         ai.Reason,
		Timestamp:      nowMetav1(),
		Provider:       "anthropic",
	}, nil
}

// validateDecision rejects decisions with unknown actions, out-of-bounds
// replica counts, or confidence values outside [0, 1].
func (a *AnthropicAgent) validateDecision(d aiDecision) error {
	switch d.Action {
	case "scale-up", "scale-down", "hold":
	default:
		return fmt.Errorf("unknown action %q", d.Action)
	}
	if d.Action != "hold" {
		if d.TargetReplicas < a.cfg.MinReplicas || d.TargetReplicas > a.cfg.MaxReplicas {
			return fmt.Errorf("targetReplicas %d out of bounds [%d, %d]",
				d.TargetReplicas, a.cfg.MinReplicas, a.cfg.MaxReplicas)
		}
	}
	if d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("confidence %.3f out of range [0.0, 1.0]", d.Confidence)
	}
	return nil
}

// messagesEndpoint returns the full URL for the Anthropic Messages API.
// cfg.BaseURL, if set, overrides the production base so tests can inject a
// local httptest server without modifying any production code paths.
func (a *AnthropicAgent) messagesEndpoint() string {
	base := anthropicDefaultEndpoint
	if a.cfg.BaseURL != "" {
		base = a.cfg.BaseURL
	}
	return base + "/v1/messages"
}

func (a *AnthropicAgent) modelName() string {
	if a.cfg.Model != "" {
		return a.cfg.Model
	}
	return anthropicDefaultModel
}

// ---- Anthropic API wire types ----------------------------------------------

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID      string                `json:"id"`
	Type    string                `json:"type"`
	Content []anthropicContent    `json:"content"`
	Model   string                `json:"model"`
	Error   *anthropicErrorDetail `json:"error,omitempty"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// aiDecision is the JSON shape the model is instructed to return.
type aiDecision struct {
	Action         string  `json:"action"`
	TargetReplicas int32   `json:"targetReplicas"`
	Confidence     float64 `json:"confidence"`
	Reason         string  `json:"reason"`
}
