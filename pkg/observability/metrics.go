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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	decisionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agentic_autoscaler_decisions_total",
		Help: "Total scaling decisions made, labelled by action, deployment, and provider.",
	}, []string{"action", "deployment", "provider"})

	decisionLatencySeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "agentic_autoscaler_decision_latency_seconds",
		Help:    "Time from signal collection to policy-enforced decision, per deployment.",
		Buckets: prometheus.DefBuckets,
	}, []string{"deployment"})

	aiProviderErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agentic_autoscaler_ai_provider_errors_total",
		Help: "Total AI provider errors that triggered a rule-based fallback.",
	}, []string{"provider"})

	cooldownSkipsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agentic_autoscaler_cooldown_skips_total",
		Help: "Total scale actions skipped because the cooldown window was active.",
	}, []string{"deployment"})
)

// RecordDecision increments decisions_total and observes decision_latency_seconds.
// latencySeconds is the elapsed time since the start of the reconcile cycle.
func RecordDecision(action, deployment, provider string, latencySeconds float64) {
	decisionsTotal.WithLabelValues(action, deployment, provider).Inc()
	decisionLatencySeconds.WithLabelValues(deployment).Observe(latencySeconds)
}

// RecordAIProviderError increments ai_provider_errors_total for the given provider.
func RecordAIProviderError(provider string) {
	aiProviderErrorsTotal.WithLabelValues(provider).Inc()
}

// RecordCooldownSkip increments cooldown_skips_total for the given deployment.
func RecordCooldownSkip(deployment string) {
	cooldownSkipsTotal.WithLabelValues(deployment).Inc()
}
