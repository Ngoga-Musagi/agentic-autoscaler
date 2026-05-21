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

package fusion

import (
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

const (
	// scaleUpLatencyThresholdMs is the p99 latency above which a non-zero
	// severity score triggers a scale-up recommendation.
	scaleUpLatencyThresholdMs = 1000.0

	// scaleUpSeverityThreshold is the minimum SeverityScore that, combined
	// with elevated latency, triggers a scale-up recommendation.
	scaleUpSeverityThreshold = 0.5

	// scaleDownCPUThreshold is the CPU utilisation percentage below which
	// a clean (zero-severity) snapshot may trigger a scale-down recommendation.
	scaleDownCPUThreshold = 20.0
)

// Correlate matches DefaultPatterns against the log entries in snapshot,
// computes a composite SeverityScore, and derives a coarse Recommendation
// for the reasoning engine.
//
// Scoring rules (evaluated in order, first match wins):
//  1. SeverityScore > 0.5 AND latency p99 > 1000 ms → "scale-up"
//  2. SeverityScore == 0.0 AND CPU < 20 % → "scale-down"
//  3. Otherwise → "hold"
func Correlate(snapshot signals.SystemSnapshot) FusedSignal {
	counts := matchPatterns(snapshot.LogEntries)

	var matched []PatternMatch
	var maxScore float64
	for _, p := range DefaultPatterns {
		count, ok := counts[p.Name]
		if !ok {
			continue
		}
		matched = append(matched, PatternMatch{
			Name:     p.Name,
			Severity: p.Severity,
			Count:    count,
		})
		if p.Score > maxScore {
			maxScore = p.Score
		}
	}
	if maxScore > 1.0 {
		maxScore = 1.0
	}

	recommendation := recommend(maxScore, snapshot.Metrics)

	return FusedSignal{
		Snapshot:        snapshot,
		MatchedPatterns: matched,
		SeverityScore:   maxScore,
		Recommendation:  recommendation,
	}
}

// matchPatterns scans all log entries against every DefaultPattern and returns
// a map from pattern name to the number of entries that matched it.
func matchPatterns(entries []signals.LogEntry) map[string]int {
	counts := make(map[string]int)
	for _, entry := range entries {
		for _, p := range DefaultPatterns {
			if p.Re.MatchString(entry.Message) {
				counts[p.Name]++
			}
		}
	}
	return counts
}

// recommend derives the coarse scaling recommendation from the severity score
// and the metric snapshot.
func recommend(severityScore float64, m signals.MetricSnapshot) string {
	switch {
	case severityScore > scaleUpSeverityThreshold && m.LatencyP99Ms > scaleUpLatencyThresholdMs:
		return "scale-up"
	case severityScore == 0.0 && m.CPUUtilPct < scaleDownCPUThreshold:
		return "scale-down"
	default:
		return "hold"
	}
}
