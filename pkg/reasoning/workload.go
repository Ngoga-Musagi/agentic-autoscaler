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
	"fmt"
	"math"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

const (
	actionScaleUp   = "scale-up"
	actionScaleDown = "scale-down"
)

// GuardWorkload enforces the agentic workload guardrails on a decision from
// any provider, so an LLM cannot reason its way past them. It only ever turns
// a scale-up into a hold, and never touches an OOM emergency:
//
//   - LLM rate limiting at or above cfg.RateLimitHoldPct: the provider's quota
//     is shared by every replica, so more workers only add calls against it.
//     The bottleneck is the dependency, not capacity.
//   - Token budget at or above cfg.TokenBudgetCapPct: the cost guardrail.
//
// Signals that are not configured (nil) or guards left at zero do nothing, so
// non-agent services are unaffected.
func GuardWorkload(cfg Config, s fusion.FusedSignal, d ScaleDecision) ScaleDecision {
	if d.Action != actionScaleUp || s.HasPattern("oom-killed") {
		return d
	}
	m := s.Snapshot.Metrics
	if p := m.LLMRateLimitedPct; p != nil && cfg.RateLimitHoldPct > 0 && *p >= cfg.RateLimitHoldPct {
		return HeldBy(d, "llm-rate-limit", fmt.Sprintf(
			"held: the LLM provider is rate-limiting %.0f%% of calls (hold at %.0f%%) — its quota is shared "+
				"by every replica, so more workers would only add calls against it", *p, cfg.RateLimitHoldPct))
	}
	if p := m.TokenBudgetUsedPct; p != nil && cfg.TokenBudgetCapPct > 0 && *p >= cfg.TokenBudgetCapPct {
		return HeldBy(d, "token-budget", fmt.Sprintf(
			"held by the cost guardrail: %.0f%% of the token budget is used (cap %.0f%%)", *p, cfg.TokenBudgetCapPct))
	}
	return d
}

// HeldBy converts a blocked scale action into a hold whose reason says what
// was blocked and why, so the on-call sees the guardrail rather than a silent
// no-op. Provider, confidence and matched patterns survive for the audit.
func HeldBy(d ScaleDecision, guardrail, why string) ScaleDecision {
	h := HoldDecision(fmt.Sprintf("%s; would have scaled to %d replicas: %s", why, d.TargetReplicas, d.Reason))
	h.Provider = d.Provider
	h.Confidence = d.Confidence
	h.OldReplicas = d.OldReplicas
	h.PatternsMatched = d.PatternsMatched
	h.Guardrail = guardrail
	return h
}

// backlogReplicas returns how many replicas the backlog calls for, and false
// when no backlog signal or per-replica target is configured.
func backlogReplicas(cfg Config, s fusion.FusedSignal) (int32, bool) {
	b := s.Snapshot.Metrics.Backlog
	if b == nil || cfg.BacklogPerReplica <= 0 {
		return 0, false
	}
	want := int32(math.Ceil(*b / float64(cfg.BacklogPerReplica)))
	if want < 1 {
		want = 1
	}
	return want, true
}
