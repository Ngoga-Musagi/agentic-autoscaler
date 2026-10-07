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

package main

import (
	"testing"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

// patternByName finds a DefaultPattern by its name.
func patternByName(name string) (fusion.Pattern, bool) {
	for _, p := range fusion.DefaultPatterns {
		if p.Name == name {
			return p, true
		}
	}
	return fusion.Pattern{}, false
}

// TestFaultLogLines_MatchFusionPatterns is the contract that keeps the demo
// honest: every line the emitter writes must actually match the operator's
// fusion pattern it is keyed on. If someone edits a line into something the
// operator would not detect, this fails — the demo would silently stop
// exercising log-pattern fusion otherwise.
func TestFaultLogLines_MatchFusionPatterns(t *testing.T) {
	if len(faultLogLines) == 0 {
		t.Fatal("faultLogLines is empty")
	}
	for name, line := range faultLogLines {
		p, ok := patternByName(name)
		if !ok {
			t.Errorf("scenario %q has no matching pattern in fusion.DefaultPatterns", name)
			continue
		}
		if !p.Re.MatchString(line) {
			t.Errorf("scenario %q: emitted line %q does not match pattern regex %q",
				name, line, p.Re.String())
		}
	}
}

// TestDefaultScenario_FiresConnectionPoolPattern pins the flagship path: the
// default scenario's line must drive the connection-pool-exhausted pattern that
// rule 2 keys on, and it must be detected by the real correlator.
func TestDefaultScenario_FiresConnectionPoolPattern(t *testing.T) {
	if defaultScenario != "connection-pool-exhausted" {
		t.Fatalf("defaultScenario: want connection-pool-exhausted, got %q", defaultScenario)
	}
	line := logLineFor(defaultScenario)

	snap := signals.SystemSnapshot{LogEntries: []signals.LogEntry{{Message: line}}}
	fused := fusion.Correlate(snap)
	if !fused.HasPattern("connection-pool-exhausted") {
		t.Fatalf("default scenario line %q was not detected as connection-pool-exhausted", line)
	}
}

// TestLogLineFor_UnknownFallsBackToDefault ensures a typo'd scenario still emits
// a valid, pattern-matching line rather than an empty string.
func TestLogLineFor_UnknownFallsBackToDefault(t *testing.T) {
	line := logLineFor("does-not-exist")
	if line != faultLogLines[defaultScenario] {
		t.Errorf("unknown scenario should fall back to the default line, got %q", line)
	}
	if knownScenario("does-not-exist") {
		t.Error("knownScenario must report false for an unknown name")
	}
}
