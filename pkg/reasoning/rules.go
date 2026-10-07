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
	"time"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

const cleanWindowDuration = 15 * time.Minute

// RuleBasedAgent makes deterministic scaling decisions from fixed threshold rules.
// It requires no external API calls and is always available as a last-resort
// fallback when cloud or self-hosted AI providers are unreachable.
//
// The agent is stateless: the sustained-quiet timer for the scale-down rule is
// carried in cfg.CleanSince, which the controller persists on the CR status and
// re-supplies every reconcile. This lets the quiet window survive the controller
// rebuilding the agent on each cycle (it must, to pick up the live replica count).
type RuleBasedAgent struct {
	cfg Config
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
//  2. connection-pool-exhausted AND p99 > 2 000 ms AND gate → scale up 50 %, confidence 0.90
//  3. error rate > 10 %                                     → scale up 30 %, confidence 0.80
//  4. circuit-breaker-open pattern AND gate                 → scale up 30 %, confidence 0.70
//  5. SeverityScore == 0 AND CPU < 20 % AND quiet ≥ 15 min  → scale down to min+1, confidence 0.60
//  6. default                                               → hold
//
// "gate" is the consecutive-window requirement: the sustained scale-up rules
// (2 and 4) fire only once a qualifying log pattern has persisted across
// cfg.ConsecutiveWindowThreshold reconciles. The emergency OOM rule (1) and the
// metric-driven error-rate rule (3) are never gated.
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

	// Rule 1: OOM kills are always an emergency — scale before the pod is
	// restarted again and hits the same wall.
	if s.HasPattern("oom-killed") {
		target := scaleUpByFraction(cur, 0.50, r.cfg.MaxReplicas)
		return ScaleUpDecision(cur, target, 0.95,
			"OOMKilled detected: adding capacity before next restart"), nil
	}

	// Rule 2: connection-pool exhaustion combined with elevated latency is a
	// strong indicator that the current replica count cannot handle load. It is
	// gated on the consecutive-window counter so a single transient window does
	// not trigger a scale-up when a sustained window is configured.
	if s.HasPattern("connection-pool-exhausted") && m.LatencyP99Ms > 2000 && r.consecutiveWindowGateMet() {
		target := scaleUpByFraction(cur, 0.50, r.cfg.MaxReplicas)
		return ScaleUpDecision(cur, target, 0.90,
			r.sustainedReason("connection pool exhausted with p99 latency spike")), nil
	}

	// Rule 3: high error rate means requests are actively failing; more capacity
	// can relieve back-pressure even if log patterns are absent.
	if m.ErrorRatePct > 10 {
		target := scaleUpByFraction(cur, 0.30, r.cfg.MaxReplicas)
		return ScaleUpDecision(cur, target, 0.80,
			"HTTP error rate exceeded 10%"), nil
	}

	// Rule 4: an open circuit breaker means downstream calls are being shed;
	// additional replicas can distribute the retry load. Like rule 2 this is a
	// sustained-pattern rule and is gated on the consecutive-window counter.
	if s.HasPattern("circuit-breaker-open") && r.consecutiveWindowGateMet() {
		target := scaleUpByFraction(cur, 0.30, r.cfg.MaxReplicas)
		return ScaleUpDecision(cur, target, 0.70,
			r.sustainedReason("circuit breaker open, distributing retry load")), nil
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

// cleanWindowReached reports whether the quiet window carried in cfg.CleanSince
// has been sustained long enough to trigger a conservative scale-down. A nil
// CleanSince means patterns are active (or the window has not yet started), so
// the window is never considered reached.
func (r *RuleBasedAgent) cleanWindowReached() bool {
	window := r.cfg.CleanWindow
	if window <= 0 {
		window = cleanWindowDuration // default 15 minutes
	}
	return r.cfg.CleanSince != nil && time.Since(r.cfg.CleanSince.Time) >= window
}

// consecutiveWindowThreshold returns the effective sustained-window requirement,
// treating an unset (zero) threshold as 1 so a CR created before the field
// existed keeps the original single-window behaviour.
func (r *RuleBasedAgent) consecutiveWindowThreshold() int32 {
	if r.cfg.ConsecutiveWindowThreshold < 1 {
		return 1
	}
	return r.cfg.ConsecutiveWindowThreshold
}

// consecutiveWindowGateMet reports whether a qualifying log pattern has been
// present for at least the configured number of consecutive reconcile windows.
// With the default threshold of 1 it is always satisfied when a pattern is
// present this window, preserving the original immediate behaviour.
func (r *RuleBasedAgent) consecutiveWindowGateMet() bool {
	return r.cfg.ConsecutivePatternWindows >= r.consecutiveWindowThreshold()
}

// sustainedReason annotates a scale-up reason with the consecutive-window count,
// but only when the gate is actually in force (threshold > 1). At the default
// threshold of 1 the base reason is returned unchanged, so the decision never
// claims "across N consecutive windows" when no multi-window gate applied.
func (r *RuleBasedAgent) sustainedReason(base string) string {
	if t := r.consecutiveWindowThreshold(); t > 1 {
		return fmt.Sprintf("%s across %d consecutive windows", base, t)
	}
	return base
}
