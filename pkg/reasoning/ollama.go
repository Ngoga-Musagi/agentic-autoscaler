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
	ollamaDefaultModel   = "llama3:8b"
	ollamaHTTPTimeout    = 30 * time.Second
	ollamaGeneratePath   = "/api/generate"
)

// OllamaAgent calls a self-hosted Ollama instance to make scaling decisions.
// It falls back to the rule-based agent immediately on any failure — no retries —
// because a blocked reconcile is worse than a conservative rule-based decision.
type OllamaAgent struct {
	cfg      Config
	client   *http.Client
	fallback Agent
}

// NewOllamaAgent returns an OllamaAgent pointed at cfg.BaseURL.
// cfg.BaseURL must be the in-cluster Ollama service URL, e.g.
// http://ollama.ollama.svc.cluster.local:11434.
func NewOllamaAgent(cfg Config) Agent {
	return &OllamaAgent{
		cfg:      cfg,
		client:   &http.Client{Timeout: ollamaHTTPTimeout},
		fallback: NewRuleBasedAgent(cfg),
	}
}

// Decide queries the Ollama /api/generate endpoint and parses the JSON decision
// from the response field. On any failure it falls through to the rule-based agent.
func (o *OllamaAgent) Decide(ctx context.Context, signal fusion.FusedSignal) (ScaleDecision, error) {
	bodyBytes, err := o.buildRequestBody(signal)
	if err != nil {
		log.Printf("WARNING: ollama: failed to build request body: %v; using rule-based fallback", err)
		return o.fallback.Decide(ctx, signal)
	}

	respBytes, err := o.doRequest(ctx, bodyBytes)
	if err != nil {
		log.Printf("WARNING: ollama: API call failed: %v; using rule-based fallback", err)
		return o.fallback.Decide(ctx, signal)
	}

	decision, err := o.parseDecision(respBytes)
	if err != nil {
		log.Printf("WARNING: ollama: response parse failed: %v; using rule-based fallback", err)
		return o.fallback.Decide(ctx, signal)
	}

	return decision, nil
}

// buildRequestBody marshals the Ollama generate request.
// stream:false requests a single JSON response object instead of NDJSON chunks.
func (o *OllamaAgent) buildRequestBody(signal fusion.FusedSignal) ([]byte, error) {
	prompt := SystemPrompt + "\n\n" + BuildPrompt(o.cfg, signal)
	req := ollamaRequest{
		Model:  o.modelName(),
		Prompt: prompt,
		Stream: false,
	}
	return json.Marshal(req)
}

// doRequest performs a single POST to the Ollama generate endpoint.
func (o *OllamaAgent) doRequest(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, o.generateEndpoint(), bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	return respBody, nil
}

// parseDecision extracts the AI's JSON from the Ollama response envelope,
// validates it, and maps it to a ScaleDecision.
func (o *OllamaAgent) parseDecision(raw []byte) (ScaleDecision, error) {
	var envelope ollamaResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ScaleDecision{}, fmt.Errorf("unmarshal ollama envelope: %w", err)
	}

	if envelope.Response == "" {
		return ScaleDecision{}, fmt.Errorf("empty response field in ollama response")
	}

	var ai aiDecision
	if err := json.Unmarshal([]byte(envelope.Response), &ai); err != nil {
		return ScaleDecision{}, fmt.Errorf("parse ai decision JSON %q: %w", envelope.Response, err)
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
		Provider:       "ollama",
	}, nil
}

// validateDecision rejects decisions with unknown actions, out-of-bounds
// replica counts, or confidence values outside [0, 1].
func (o *OllamaAgent) validateDecision(d aiDecision) error {
	switch d.Action {
	case "scale-up", "scale-down", "hold":
	default:
		return fmt.Errorf("unknown action %q", d.Action)
	}
	if d.Action != "hold" {
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

func (o *OllamaAgent) generateEndpoint() string {
	return o.cfg.BaseURL + ollamaGeneratePath
}

func (o *OllamaAgent) modelName() string {
	if o.cfg.Model != "" {
		return o.cfg.Model
	}
	return ollamaDefaultModel
}

// ---- Ollama API wire types -------------------------------------------------

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
	Done     bool   `json:"done"`
}
