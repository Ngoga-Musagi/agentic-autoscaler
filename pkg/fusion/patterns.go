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

import "regexp"

// Pattern is a named log pattern with a compiled regex, severity, and a
// contribution weight toward the overall SeverityScore.
type Pattern struct {
	// Name is the stable identifier used in PatternMatch and Grafana annotations.
	Name string

	// Re is the compiled regular expression matched against each log message.
	Re *regexp.Regexp

	// Severity is "warning" or "critical".
	Severity string

	// Score is this pattern's contribution to SeverityScore (0.0–1.0).
	Score float64
}

// DefaultPatterns is the built-in set of patterns the correlator checks.
// Patterns are matched case-sensitively against raw log messages after ANSI
// stripping; the ordering does not matter — all patterns are evaluated.
var DefaultPatterns = []Pattern{
	{
		Name:     "connection-pool-exhausted",
		Re:       regexp.MustCompile(`connection pool exhausted|POOL_EXHAUSTED`),
		Severity: "critical",
		Score:    0.8,
	},
	{
		Name:     "upstream-timeout",
		Re:       regexp.MustCompile(`upstream timeout|ETIMEDOUT|context deadline exceeded`),
		Severity: "warning",
		Score:    0.5,
	},
	{
		Name:     "oom-killed",
		Re:       regexp.MustCompile(`OOMKilled|out of memory`),
		Severity: "critical",
		Score:    0.9,
	},
	{
		Name:     "circuit-breaker-open",
		Re:       regexp.MustCompile(`circuit.*open|CircuitBreaker.*OPEN`),
		Severity: "warning",
		Score:    0.6,
	},
	{
		Name:     "db-connection-failed",
		Re:       regexp.MustCompile(`dial tcp.*connection refused|database.*unreachable`),
		Severity: "critical",
		Score:    0.7,
	},
}
