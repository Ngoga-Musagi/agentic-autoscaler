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

import "testing"

func TestFusedSignal_HasPattern(t *testing.T) {
	matched := []PatternMatch{
		{Name: "connection-pool-exhausted", Severity: "critical", Count: 2},
		{Name: "upstream-timeout", Severity: "warning", Count: 1},
	}
	sig := FusedSignal{MatchedPatterns: matched}

	if !sig.HasPattern("connection-pool-exhausted") {
		t.Error("HasPattern: expected true for present pattern connection-pool-exhausted")
	}
	if !sig.HasPattern("upstream-timeout") {
		t.Error("HasPattern: expected true for present pattern upstream-timeout")
	}
}

func TestFusedSignal_HasPattern_Absent(t *testing.T) {
	matched := []PatternMatch{
		{Name: "oom-killed", Severity: "critical", Count: 1},
	}
	sig := FusedSignal{MatchedPatterns: matched}

	if sig.HasPattern("connection-pool-exhausted") {
		t.Error("HasPattern: expected false for absent pattern connection-pool-exhausted")
	}
}

func TestFusedSignal_HasPattern_Empty(t *testing.T) {
	sig := FusedSignal{}

	if sig.HasPattern("oom-killed") {
		t.Error("HasPattern: expected false when MatchedPatterns is empty")
	}
}
