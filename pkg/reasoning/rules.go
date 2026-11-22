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
// metric-driven error-rate rule (3) are never gated. Rule B (backlog → scale up
// to ceil(backlog / targetPerReplica), confidence 0.85) sits after rule 1 and
// exists only when spec.backlog is configured, so for every other service the
// order is exactly rules 1–6. Finally GuardWorkload may turn a scale-up into a hold (provider
// rate limiting, token budget) — again only when those signals are configured.
func (r *RuleBasedAgent) Decide(ctx context.Context, s fusion.FusedSignal) (ScaleDecision, error) {
	d, err := r.decide(ctx, s)
	if err != nil {
		return d, err
	}
	d = GuardWorkload(r.cfg, s, d)
	d.Provider = providerRuleBased
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

	// Rule B: backlog (only when spec.backlog is configured). Queued work that
	// the current replicas cannot carry at the target per-replica depth needs
	// more workers, whatever CPU says — queue workers and agent runners wait on
	// I/O. Without a backlog signal this rule never fires, so the original rule
	// order is unchanged for every other service.
	if want, ok := backlogReplicas(r.cfg, s); ok && want > cur && cur < r.cfg.MaxReplicas {
		target := want
		if target > r.cfg.MaxReplicas {
			target = r.cfg.MaxReplicas
		}
		b := *m.Backlog
		return ScaleUpDecision(cur, target, 0.85, fmt.Sprintf(
			"backlog of %.0f queued items is %.1f per replica against a target of %d",
			b, b/float64(cur), r.cfg.BacklogPerReplica)), nil
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
		// With a backlog signal, never release capacity the queue still needs.
		if want, ok := backlogReplicas(r.cfg, s); ok && want > target {
			target = cur
		}
		if cur > target {
			return ScaleDownDecision(cur, target, 0.60,
				fmt.Sprintf("all signals healthy for %s, releasing excess capacity", atLeast(r.cleanWindow()))), nil
		}
	}

	return hold("no action threshold met")
}

// cleanWindowReached reports whether the quiet window carried in cfg.CleanSince
// has been sustained long enough to trigger a conservative scale-down. A nil
// CleanSince means patterns are active (or the window has not yet started), so
// the window is never considered reached.
func (r *RuleBasedAgent) cleanWindowReached() bool {
	return r.cfg.CleanSince != nil && time.Since(r.cfg.CleanSince.Time) >= r.cleanWindow()
}

// cleanWindow is the configured quiet window, defaulting to 15 minutes.
func (r *RuleBasedAgent) cleanWindow() time.Duration {
	if r.cfg.CleanWindow <= 0 {
		return cleanWindowDuration
	}
	return r.cfg.CleanWindow
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

// atLeast renders a quiet window the way an on-call reads it ("15+ minutes",
// "1+ minute"); windows that are not whole minutes use Go's notation.
func atLeast(d time.Duration) string {
	if d%time.Minute != 0 {
		return d.String() + "+"
	}
	if m := int(d / time.Minute); m != 1 {
		return fmt.Sprintf("%d+ minutes", m)
	}
	return "1+ minute"
}
