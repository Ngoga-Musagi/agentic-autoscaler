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

package controller

import (
	"testing"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

func patternByName(patterns []fusion.Pattern, name string) (fusion.Pattern, bool) {
	for _, p := range patterns {
		if p.Name == name {
			return p, true
		}
	}
	return fusion.Pattern{}, false
}

func TestEffectivePatterns_EmptyReturnsDefaultsCopy(t *testing.T) {
	patterns, warnings := effectivePatterns(nil)
	if len(warnings) != 0 {
		t.Errorf("warnings: want none, got %v", warnings)
	}
	if len(patterns) != len(fusion.DefaultPatterns) {
		t.Fatalf("pattern count: want %d (defaults), got %d", len(fusion.DefaultPatterns), len(patterns))
	}
	// Must be a copy, not the package slice aliased.
	if &patterns[0] == &fusion.DefaultPatterns[0] {
		t.Error("effectivePatterns must not alias the package-level DefaultPatterns slice")
	}
}

func TestEffectivePatterns_MergeAppendsNewName(t *testing.T) {
	patterns, warnings := effectivePatterns([]scalingv1alpha1.LogPatternSpec{
		{Name: "tls-handshake-timeout", Regex: `tls: handshake timeout`, Severity: "critical", Score: 80},
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings: want none, got %v", warnings)
	}
	if len(patterns) != len(fusion.DefaultPatterns)+1 {
		t.Fatalf("pattern count: want defaults+1, got %d", len(patterns))
	}
	p, ok := patternByName(patterns, "tls-handshake-timeout")
	if !ok {
		t.Fatal("custom pattern not merged in")
	}
	if p.Score != 0.80 {
		t.Errorf("score mapping: want 0.80 from spec 80, got %.2f", p.Score)
	}
}

func TestEffectivePatterns_OverrideReplacesBuiltin(t *testing.T) {
	// Reusing a built-in name overrides it rather than duplicating.
	patterns, _ := effectivePatterns([]scalingv1alpha1.LogPatternSpec{
		{Name: "connection-pool-exhausted", Regex: `POOL DRAINED`, Severity: "critical", Score: 90},
	})
	if len(patterns) != len(fusion.DefaultPatterns) {
		t.Fatalf("override must not change count: want %d, got %d", len(fusion.DefaultPatterns), len(patterns))
	}
	p, ok := patternByName(patterns, "connection-pool-exhausted")
	if !ok {
		t.Fatal("overridden pattern missing")
	}
	if !p.Re.MatchString("POOL DRAINED at 12:00") {
		t.Error("override regex not applied")
	}
	if p.Re.MatchString("connection pool exhausted") {
		t.Error("built-in regex should have been replaced by the override")
	}
}

func TestEffectivePatterns_InvalidRegexSkippedAndReported(t *testing.T) {
	patterns, warnings := effectivePatterns([]scalingv1alpha1.LogPatternSpec{
		{Name: "bad", Regex: `[unclosed`, Severity: "warning", Score: 50},
		{Name: "good", Regex: `works`, Severity: "warning", Score: 50},
	})
	if len(warnings) != 1 {
		t.Fatalf("warnings: want 1 (for the bad pattern), got %d: %v", len(warnings), warnings)
	}
	if _, ok := patternByName(patterns, "bad"); ok {
		t.Error("invalid pattern must be skipped, not added")
	}
	if _, ok := patternByName(patterns, "good"); !ok {
		t.Error("valid pattern after the invalid one must still be added")
	}
	// Defaults are preserved alongside the one valid custom pattern.
	if len(patterns) != len(fusion.DefaultPatterns)+1 {
		t.Errorf("pattern count: want defaults+1 (bad skipped), got %d", len(patterns))
	}
}
