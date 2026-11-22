package fusion

import (
	"testing"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

func TestCompilePattern_Valid(t *testing.T) {
	p, err := CompilePattern("tls-handshake-timeout", `tls: handshake timeout`, "critical", 0.75)
	if err != nil {
		t.Fatalf("CompilePattern: unexpected error: %v", err)
	}
	if p.Name != "tls-handshake-timeout" || p.Severity != "critical" || p.Score != 0.75 {
		t.Errorf("unexpected pattern fields: %+v", p)
	}
	if !p.Re.MatchString("error: tls: handshake timeout after 10s") {
		t.Error("compiled regex did not match an expected line")
	}
}

func TestCompilePattern_InvalidRegex(t *testing.T) {
	// An unclosed character class must be reported, never panic.
	if _, err := CompilePattern("bad", `[unclosed`, "warning", 0.5); err == nil {
		t.Fatal("CompilePattern: want error for invalid regex, got nil")
	}
}

func TestCompilePattern_InvalidSeverity(t *testing.T) {
	if _, err := CompilePattern("x", `ok`, "info", 0.5); err == nil {
		t.Fatal("CompilePattern: want error for invalid severity, got nil")
	}
}

func TestCompilePattern_ScoreOutOfRange(t *testing.T) {
	for _, score := range []float64{-0.1, 1.5} {
		if _, err := CompilePattern("x", `ok`, "warning", score); err == nil {
			t.Errorf("CompilePattern: want error for score %v, got nil", score)
		}
	}
}

func TestCompilePattern_EmptyName(t *testing.T) {
	if _, err := CompilePattern("", `ok`, "warning", 0.5); err == nil {
		t.Fatal("CompilePattern: want error for empty name, got nil")
	}
}

// TestCorrelateWithPatterns_CustomPattern proves a caller-supplied pattern set
// drives the fused result: a custom pattern matches a synthetic log line and
// contributes its score to SeverityScore.
func TestCorrelateWithPatterns_CustomPattern(t *testing.T) {
	custom, err := CompilePattern("dns-resolution-failed", `no such host|SERVFAIL`, "critical", 0.85)
	if err != nil {
		t.Fatalf("CompilePattern: %v", err)
	}
	snap := snapshot(
		signals.MetricSnapshot{LatencyP99Ms: 1500, CPUUtilPct: 60},
		"lookup payments.internal: no such host",
	)

	got := CorrelateWithPatterns(snap, []Pattern{custom})
	if !hasPattern(got.MatchedPatterns, "dns-resolution-failed") {
		t.Fatalf("custom pattern not matched, got %v", patternNames(got.MatchedPatterns))
	}
	if got.SeverityScore != 0.85 {
		t.Errorf("SeverityScore: want 0.85 from custom pattern, got %.2f", got.SeverityScore)
	}
	if got.Recommendation != "scale-up" {
		t.Errorf("Recommendation: want scale-up (severity>0.5, latency>1000), got %q", got.Recommendation)
	}
}

// TestCorrelateWithPatterns_EmptySetMatchesNothing confirms that with no
// patterns the correlator is inert (severity 0) rather than erroring.
func TestCorrelateWithPatterns_EmptySetMatchesNothing(t *testing.T) {
	snap := snapshot(
		signals.MetricSnapshot{LatencyP99Ms: 3000, CPUUtilPct: 90},
		"connection pool exhausted",
	)
	got := CorrelateWithPatterns(snap, nil)
	if len(got.MatchedPatterns) != 0 || got.SeverityScore != 0 {
		t.Errorf("empty pattern set must match nothing, got %v (severity %.2f)",
			patternNames(got.MatchedPatterns), got.SeverityScore)
	}
}

func TestAgenticPatterns_MatchAgentFailureLinesAndStayDistinct(t *testing.T) {
	lines := map[string]string{
		"llm-rate-limited":      "WARN LLM rate limited (HTTP 429) on attempt 2, backing off",
		"tool-timeout":          "ERROR tool web-search timed out after 2s",
		"context-overflow":      "WARN context length exceeded: prompt 131072 tokens > 128000 limit",
		"agent-max-iterations":  "agent stopped: max iterations reached (25)",
		"worker-pool-saturated": "WARN agent worker pool saturated: all 4 workers busy, task queued 2300ms",
	}
	builtin := map[string]bool{}
	for _, p := range DefaultPatterns {
		builtin[p.Name] = true
	}
	for _, p := range AgenticPatterns {
		if builtin[p.Name] {
			t.Errorf("%s collides with a default pattern name", p.Name)
		}
		line, ok := lines[p.Name]
		if !ok {
			t.Errorf("no sample line for %s", p.Name)
			continue
		}
		for _, other := range AgenticPatterns {
			if got := other.Re.MatchString(line); got != (other.Name == p.Name) {
				t.Errorf("line for %s: match %s = %v", p.Name, other.Name, got)
			}
		}
	}
}
