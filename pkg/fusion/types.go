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

// Package fusion correlates Prometheus metrics with log stream patterns to
// produce a FusedSignal that the reasoning engine can act on.
//
// This package has zero Kubernetes dependencies — it is pure Go.
package fusion

import (
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

// FusedSignal is the output of the correlation step. It combines the raw
// SystemSnapshot with the log patterns that matched and a scalar severity score
// that the reasoning engine uses to decide whether to scale.
type FusedSignal struct {
	// Snapshot is the original metrics + log data this signal was derived from.
	Snapshot signals.SystemSnapshot

	// MatchedPatterns lists every log pattern that fired at least once.
	MatchedPatterns []PatternMatch

	// SeverityScore is a normalised 0.0 (healthy) → 1.0 (critical) value
	// computed as the maximum score across all matched patterns.
	SeverityScore float64

	// Recommendation is the correlator's coarse suggestion to the reasoning
	// engine: "scale-up", "scale-down", or "hold".
	Recommendation string
}

// HasPattern reports whether the named pattern fired at least once during correlation.
func (s FusedSignal) HasPattern(name string) bool {
	for _, p := range s.MatchedPatterns {
		if p.Name == name {
			return true
		}
	}
	return false
}

// PatternMatch records a single pattern that was found in the log stream.
type PatternMatch struct {
	// Name is the pattern identifier, e.g. "connection-pool-exhausted".
	Name string

	// Severity is either "warning" or "critical".
	Severity string

	// Count is how many log entries matched this pattern.
	Count int
}
