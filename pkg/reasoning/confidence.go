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
	"fmt"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

const providerRuleBased = "rule-based"

// WithConfidenceFloor wraps an AI agent so that a decision whose confidence is
// below floor (0-1) is discarded and fallback — the deterministic rule engine —
// decides instead (spec.policy.minConfidence). Rule-based decisions pass
// through: the floor is a check on the model, not on the rules.
func WithConfidenceFloor(agent, fallback Agent, floor float64) Agent {
	return &confidenceFloor{agent: agent, fallback: fallback, floor: floor}
}

type confidenceFloor struct {
	agent, fallback Agent
	floor           float64
}

// Decide asks the wrapped agent and falls back to the rules below the floor.
func (c *confidenceFloor) Decide(ctx context.Context, s fusion.FusedSignal) (ScaleDecision, error) {
	d, err := c.agent.Decide(ctx, s)
	if err != nil || d.Provider == providerRuleBased || d.Confidence >= c.floor {
		return d, err
	}
	r, err := c.fallback.Decide(ctx, s)
	if err != nil {
		return r, err
	}
	r.Reason = fmt.Sprintf("%s confidence %.2f is below the %.2f floor, so the rule engine decided: %s",
		d.Provider, d.Confidence, c.floor, r.Reason)
	r.Guardrail = "min-confidence"
	return r, nil
}
