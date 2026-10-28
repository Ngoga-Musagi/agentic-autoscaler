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

package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordDecision_DoesNotPanic(t *testing.T) {
	RecordDecision("scale-up", "payment-service", "rule-based", 0.1)
	RecordDecision("scale-down", "checkout", "anthropic", 1.5)
	RecordDecision("hold", "api", "ollama", 0.05)
}

func TestRecordAIProviderError_DoesNotPanic(t *testing.T) {
	RecordAIProviderError("anthropic")
	RecordAIProviderError("openai")
	RecordAIProviderError("ollama")
}

func TestRecordCooldownSkip_DoesNotPanic(t *testing.T) {
	RecordCooldownSkip("payment-service")
	RecordCooldownSkip("checkout")
}

func TestRecommendedReplicas_SetAndClear(t *testing.T) {
	series := func() float64 {
		return testutil.ToFloat64(recommendedReplicas.WithLabelValues("prod", "web-autoscaler", "prod", "web"))
	}
	SetRecommendedReplicas("prod", "web-autoscaler", "prod", "web", 7)
	if got := series(); got != 7 {
		t.Fatalf("recommended replicas: want 7, got %v", got)
	}
	SetRecommendedReplicas("prod", "other", "prod", "other", 3)

	ClearRecommendedReplicas("prod", "web-autoscaler")
	if n := testutil.CollectAndCount(recommendedReplicas); n != 1 {
		t.Errorf("after clear: want only the other autoscaler's series, got %d series", n)
	}
	ClearRecommendedReplicas("prod", "other")
}
