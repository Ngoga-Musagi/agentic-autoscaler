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

// faultLogLines maps a fusion pattern name to a log line the emitter writes to
// stdout while that fault scenario is active. Each line is crafted to match the
// corresponding pattern in pkg/fusion.DefaultPatterns, so the operator's
// Loki → fusion path detects it end to end.
//
// These lines live here, in an independent demo workload — never inside the
// operator — so the timeout-storm demo genuinely exercises log ingestion and
// fusion rather than a simulated shortcut (T4.1 STOP condition). The emit unit
// test verifies every line actually matches the pattern it is keyed on.
var faultLogLines = map[string]string{
	"connection-pool-exhausted": "ERROR connection pool exhausted: all 50 db connections in use, request queued 4200ms",
	"upstream-timeout":          "WARN upstream timeout: context deadline exceeded calling auth-service",
	"circuit-breaker-open":      "ERROR CircuitBreaker payment-gateway state=OPEN after 5 consecutive failures",
	"oom-killed":                "FATAL OOMKilled: worker exceeded memory limit, process out of memory",
}

// defaultScenario is the flagship "timeout storm": it emits the
// connection-pool-exhausted line which, combined with the high p99 latency the
// emitter injects, fires the rule-based agent's rule 2.
const defaultScenario = "connection-pool-exhausted"

// logLineFor returns the fault log line for a scenario, falling back to the
// default scenario's line when the name is unknown so the emitter never goes
// silent on a typo.
func logLineFor(scenario string) string {
	if line, ok := faultLogLines[scenario]; ok {
		return line
	}
	return faultLogLines[defaultScenario]
}

// knownScenario reports whether a scenario name has a dedicated log line.
func knownScenario(scenario string) bool {
	_, ok := faultLogLines[scenario]
	return ok
}
