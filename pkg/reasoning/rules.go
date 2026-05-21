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
	"time"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

const cleanWindowDuration = 15 * time.Minute

// RuleBasedAgent makes deterministic scaling decisions from fixed threshold rules.
// It requires no external API calls and is always available as a last-resort
// fallback when cloud or self-hosted AI providers are unreachable.
//
// The agent is intentionally stateful: noPatternSince tracks when log patterns
// were last observed so that the scale-down rule can enforce a quiet window.
// The controller must hold the same agent instance across reconcile cycles —
// do not recreate it on every call to NewRuleBasedAgent.
type RuleBasedAgent struct {
	cfg Config

	// noPatternSince is the wall-clock time at which the last matched pattern
	// disappeared. Zero means patterns are currently active (or we just started).
	noPatternSince time.Time
}

// NewRuleBasedAgent returns a RuleBasedAgent configured with the bounds from cfg.
func NewRuleBasedAgent(cfg Config) Agent {
	return &RuleBasedAgent{cfg: cfg}
}

// Decide applies six threshold rules in strict priority order and returns the
// first matching decision. Rules are evaluated top-to-bottom; only one fires.
//
// Priority:
//  1. oom-killed pattern                                    → scale up 50 %, confidence 0.95
//  2. connection-pool-exhausted AND latency p99 > 2 000 ms  → scale up 50 %, confidence 0.90
//  3. error rate > 10 %                                     → scale up 30 %, confidence 0.80
//  4. circuit-breaker-open pattern                          → scale up 30 %, confidence 0.70
//  5. SeverityScore == 0 AND CPU < 20 % AND quiet ≥ 15 min  → scale down to min+1, confidence 0.60
//  6. default                                               → hold
func (r *RuleBasedAgent) Decide(ctx context.Context, s fusion.FusedSignal) (ScaleDecision, error) {
	d, err := r.decide(ctx, s)
	if err != nil {
		return d, err
	}
	d.Provider = "rule-based"
	return d, nil
}

func (r *RuleBasedAgent) decide(_ context.Context, s fusion.FusedSignal) (ScaleDecision, error) {
	m := s.Snapshot.Metrics
	cur := r.cfg.CurrentReplicas

	// Maintain the "no patterns since" timer before evaluating any rule so the
	// clock starts immediately when the last pattern clears.
	r.updatePatternTimer(len(s.MatchedPatterns) > 0)

	// Rule 1: OOM kills are always an emergency — scale before the pod is
	// restarted again and hits the same wall.
	if s.HasPattern("oom-killed") {
		target := scaleUpByFraction(cur, 0.50, r.cfg.MaxReplicas)
		return ScaleUpDecision(cur, target, 0.95,
			"OOMKilled detected: adding capacity before next restart"), nil
	}

	// Rule 2: connection-pool exhaustion combined with elevated latency is a
	// strong indicator that the current replica count cannot handle load.
	if s.HasPattern("connection-pool-exhausted") && m.LatencyP99Ms > 2000 {
		target := scaleUpByFraction(cur, 0.50, r.cfg.MaxReplicas)
		return ScaleUpDecision(cur, target, 0.90,
			"connection pool exhausted with p99 latency spike"), nil
	}

	// Rule 3: high error rate means requests are actively failing; more capacity
	// can relieve back-pressure even if log patterns are absent.
	if m.ErrorRatePct > 10 {
		target := scaleUpByFraction(cur, 0.30, r.cfg.MaxReplicas)
		return ScaleUpDecision(cur, target, 0.80,
			"HTTP error rate exceeded 10%"), nil
	}

	// Rule 4: an open circuit breaker means downstream calls are being shed;
	// additional replicas can distribute the retry load.
	if s.HasPattern("circuit-breaker-open") {
		target := scaleUpByFraction(cur, 0.30, r.cfg.MaxReplicas)
		return ScaleUpDecision(cur, target, 0.70,
			"circuit breaker open, distributing retry load"), nil
	}

	// Rule 5: release capacity conservatively only after a sustained quiet
	// window — scale down to min+1 rather than min to keep one replica of
	// headroom above the hard floor.
	if s.SeverityScore == 0.0 &&
		m.CPUUtilPct < 20 &&
		r.cleanWindowReached() {
		target := r.cfg.MinReplicas + 1
		if target > r.cfg.MaxReplicas {
			target = r.cfg.MaxReplicas
		}
		if cur > target {
			return ScaleDownDecision(cur, target, 0.60,
				"all signals healthy for 15+ minutes, releasing excess capacity"), nil
		}
	}

	return hold("no action threshold met")
}

// updatePatternTimer advances the internal quiet-window clock.
// Called once per Decide invocation before any rule is evaluated.
func (r *RuleBasedAgent) updatePatternTimer(hasPatterns bool) {
	if hasPatterns {
		// Patterns are active: reset the clock so the quiet window must restart
		// from scratch the next time conditions clear.
		r.noPatternSince = time.Time{}
		return
	}
	if r.noPatternSince.IsZero() {
		// First reconcile with no patterns: start the clock now.
		r.noPatternSince = time.Now()
	}
}

// cleanWindowReached reports whether the quiet window has been sustained long
// enough to trigger a conservative scale-down.
func (r *RuleBasedAgent) cleanWindowReached() bool {
	return !r.noPatternSince.IsZero() && time.Since(r.noPatternSince) >= cleanWindowDuration
}
