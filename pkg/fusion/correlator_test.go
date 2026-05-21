package fusion

import (
	"regexp"
	"testing"
	"time"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

// entry is a convenience constructor for a log entry with just a message.
func entry(msg string) signals.LogEntry {
	return signals.LogEntry{Timestamp: time.Now(), Message: msg}
}

// snapshot assembles a SystemSnapshot from metrics and a variadic list of log messages.
func snapshot(m signals.MetricSnapshot, messages ...string) signals.SystemSnapshot {
	entries := make([]signals.LogEntry, len(messages))
	for i, msg := range messages {
		entries[i] = entry(msg)
	}
	return signals.SystemSnapshot{
		Timestamp:  time.Now(),
		Metrics:    m,
		LogEntries: entries,
	}
}

// ---- table-driven tests ----------------------------------------------------

func TestCorrelate(t *testing.T) {
	tests := []struct {
		name               string
		snap               signals.SystemSnapshot
		wantRecommendation string
		wantSeverityAbove  float64 // SeverityScore must be > this value
		wantPatterns       []string
		wantNoPatterns     []string
	}{
		{
			name: "connection-pool-exhausted + high latency → scale-up",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 2500, ErrorRatePct: 8, CPUUtilPct: 60},
				"connection pool exhausted after 30s waiting for slot",
				"connection pool exhausted again",
			),
			wantRecommendation: "scale-up",
			wantSeverityAbove:  0.5,
			wantPatterns:       []string{"connection-pool-exhausted"},
		},
		{
			name: "oom-killed log + high latency → scale-up (highest severity)",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 1500, CPUUtilPct: 80},
				"OOMKilled: pod payment-service-7d9f8b killed",
			),
			wantRecommendation: "scale-up",
			wantSeverityAbove:  0.8,
			wantPatterns:       []string{"oom-killed"},
		},
		{
			name: "no patterns + low CPU → scale-down",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 45, ErrorRatePct: 0, CPUUtilPct: 8},
				// log lines that do not match any pattern
				"request completed in 12ms",
				"health check OK",
			),
			wantRecommendation: "scale-down",
			wantSeverityAbove:  -1, // score is 0.0; use -1 so the > check always passes
			wantPatterns:       []string{},
		},
		{
			name: "warning patterns but latency within threshold → hold",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 300, CPUUtilPct: 55},
				"upstream timeout waiting for auth-service",
			),
			wantRecommendation: "hold",
			wantSeverityAbove:  -1,
			wantPatterns:       []string{"upstream-timeout"},
		},
		{
			name: "circuit-breaker-open at normal latency → hold",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 200, CPUUtilPct: 40},
				"CircuitBreaker payment-gateway OPEN after 5 failures",
			),
			wantRecommendation: "hold",
			wantPatterns:       []string{"circuit-breaker-open"},
		},
		{
			name: "db-connection-failed + high latency → scale-up",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 3200, CPUUtilPct: 70},
				"dial tcp 10.0.0.5:5432: connection refused",
			),
			wantRecommendation: "scale-up",
			wantSeverityAbove:  0.5,
			wantPatterns:       []string{"db-connection-failed"},
		},
		{
			name: "multiple patterns — score is max not sum",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 1800, CPUUtilPct: 75},
				"upstream timeout on checkout",       // score 0.5
				"circuit.*open: payment circuit open", // score 0.6
			),
			wantRecommendation: "scale-up",
			wantSeverityAbove:  0.5,
			wantPatterns:       []string{"upstream-timeout"},
		},
		{
			name: "no log entries + high CPU → hold (severity 0, CPU not low)",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 100, CPUUtilPct: 85},
			),
			wantRecommendation: "hold",
			wantSeverityAbove:  -1, // score is 0.0; -1 so the > check always passes
			wantPatterns:       []string{},
		},
		{
			name: "no log entries + zero CPU → scale-down",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 10, CPUUtilPct: 0},
			),
			wantRecommendation: "scale-down",
			wantSeverityAbove:  -1, // score is 0.0
			wantPatterns:       []string{},
		},
		{
			name: "POOL_EXHAUSTED uppercase variant matches connection-pool-exhausted",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 1200, CPUUtilPct: 60},
				"POOL_EXHAUSTED: no connections available",
			),
			wantRecommendation: "scale-up",
			wantPatterns:       []string{"connection-pool-exhausted"},
		},
		{
			name: "database unreachable matches db-connection-failed",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 2000, CPUUtilPct: 50},
				"database payment-db unreachable after 3 retries",
			),
			wantRecommendation: "scale-up",
			wantPatterns:       []string{"db-connection-failed"},
		},
		{
			name: "out of memory matches oom-killed",
			snap: snapshot(
				signals.MetricSnapshot{LatencyP99Ms: 1100, CPUUtilPct: 90},
				"out of memory: kill process 1234",
			),
			wantRecommendation: "scale-up",
			wantPatterns:       []string{"oom-killed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Correlate(tc.snap)

			if got.Recommendation != tc.wantRecommendation {
				t.Errorf("Recommendation: want %q, got %q (severity=%.2f, latency=%.0fms, cpu=%.0f%%)",
					tc.wantRecommendation, got.Recommendation,
					got.SeverityScore, tc.snap.Metrics.LatencyP99Ms, tc.snap.Metrics.CPUUtilPct)
			}

			if got.SeverityScore <= tc.wantSeverityAbove {
				t.Errorf("SeverityScore: want > %.2f, got %.2f", tc.wantSeverityAbove, got.SeverityScore)
			}
			if got.SeverityScore > 1.0 {
				t.Errorf("SeverityScore exceeds 1.0: got %.2f", got.SeverityScore)
			}

			for _, wantName := range tc.wantPatterns {
				if !hasPattern(got.MatchedPatterns, wantName) {
					t.Errorf("expected pattern %q in MatchedPatterns, got %v", wantName, patternNames(got.MatchedPatterns))
				}
			}
			for _, noName := range tc.wantNoPatterns {
				if hasPattern(got.MatchedPatterns, noName) {
					t.Errorf("did not expect pattern %q in MatchedPatterns", noName)
				}
			}

			// Snapshot must be preserved verbatim.
			if got.Snapshot.Metrics.LatencyP99Ms != tc.snap.Metrics.LatencyP99Ms {
				t.Errorf("Snapshot not preserved: latency mismatch")
			}
		})
	}
}

// ---- pattern count tests ---------------------------------------------------

func TestCorrelate_PatternCount(t *testing.T) {
	snap := snapshot(
		signals.MetricSnapshot{LatencyP99Ms: 2000},
		"connection pool exhausted",
		"connection pool exhausted again",
		"connection pool exhausted once more",
	)
	got := Correlate(snap)

	for _, pm := range got.MatchedPatterns {
		if pm.Name == "connection-pool-exhausted" {
			if pm.Count != 3 {
				t.Errorf("Count: want 3, got %d", pm.Count)
			}
			return
		}
	}
	t.Error("pattern connection-pool-exhausted not found in MatchedPatterns")
}

func TestCorrelate_SeverityScore_IsMaxNotSum(t *testing.T) {
	// upstream-timeout (0.5) + circuit-breaker-open (0.6) → max is 0.6, not 1.1
	snap := snapshot(
		signals.MetricSnapshot{LatencyP99Ms: 500},
		"upstream timeout on auth-service",
		"CircuitBreaker payment OPEN",
	)
	got := Correlate(snap)
	if got.SeverityScore != 0.6 {
		t.Errorf("SeverityScore: want 0.6 (max), got %.2f", got.SeverityScore)
	}
}

func TestCorrelate_SeverityScore_NeverExceedsOne(t *testing.T) {
	snap := snapshot(
		signals.MetricSnapshot{LatencyP99Ms: 5000},
		"OOMKilled: critical failure",
	)
	got := Correlate(snap)
	if got.SeverityScore > 1.0 {
		t.Errorf("SeverityScore > 1.0: got %.2f", got.SeverityScore)
	}
}

// TestCorrelate_SeverityScore_ClampedToOne exercises the defensive branch that
// clamps maxScore to 1.0 when a misconfigured pattern carries a score above 1.0.
// DefaultPatterns keeps all scores ≤ 0.9, so this test temporarily injects a
// pattern with Score 1.5 to reach that branch.
func TestCorrelate_SeverityScore_ClampedToOne(t *testing.T) {
	saved := DefaultPatterns
	defer func() { DefaultPatterns = saved }()

	DefaultPatterns = []Pattern{{
		Name:     "over-range",
		Re:       regexp.MustCompile(`over-range signal`),
		Severity: "critical",
		Score:    1.5, // deliberately above 1.0 to trigger the clamp
	}}

	snap := snapshot(
		signals.MetricSnapshot{LatencyP99Ms: 2000, CPUUtilPct: 80},
		"over-range signal detected",
	)
	got := Correlate(snap)
	if got.SeverityScore != 1.0 {
		t.Errorf("SeverityScore: want 1.0 (clamped), got %.2f", got.SeverityScore)
	}
}

// ---- helpers ---------------------------------------------------------------

func hasPattern(patterns []PatternMatch, name string) bool {
	for _, p := range patterns {
		if p.Name == name {
			return true
		}
	}
	return false
}

func patternNames(patterns []PatternMatch) []string {
	names := make([]string, len(patterns))
	for i, p := range patterns {
		names[i] = p.Name
	}
	return names
}
