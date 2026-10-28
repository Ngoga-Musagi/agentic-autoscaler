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
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// controller-runtime serves /metrics from its own registry, not the client_golang
// default one, so collectors must be registered there or they are never scraped.
var factory = promauto.With(ctrlmetrics.Registry)

var (
	decisionsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "agentic_autoscaler_decisions_total",
		Help: "Total scaling decisions made, labelled by action, deployment, and provider.",
	}, []string{"action", "deployment", "provider"})

	decisionLatencySeconds = factory.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "agentic_autoscaler_decision_latency_seconds",
		Help:    "Time from signal collection to policy-enforced decision, per deployment.",
		Buckets: prometheus.DefBuckets,
	}, []string{"deployment"})

	aiProviderErrorsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "agentic_autoscaler_ai_provider_errors_total",
		Help: "Total AI provider errors that triggered a rule-based fallback.",
	}, []string{"provider"})

	cooldownSkipsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "agentic_autoscaler_cooldown_skips_total",
		Help: "Total scale actions skipped because the cooldown window was active.",
	}, []string{"deployment"})

	// Labels avoid "namespace" because Prometheus rewrites a scraped namespace
	// label to exported_namespace, which would break copy-pasted HPA/KEDA queries.
	recommendedReplicas = factory.NewGaugeVec(prometheus.GaugeOpts{
		Name: "agentic_autoscaler_recommended_replicas",
		Help: "Advisor mode: replica recommendation for an HPA or KEDA ScaledObject (target averageValue 1).",
	}, []string{"autoscaler_namespace", "autoscaler", "deployment_namespace", "deployment"})
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

// SetRecommendedReplicas publishes an advisor-mode recommendation for the
// autoscaler's target Deployment.
func SetRecommendedReplicas(autoscalerNS, autoscaler, deploymentNS, deployment string, replicas int32) {
	recommendedReplicas.WithLabelValues(autoscalerNS, autoscaler, deploymentNS, deployment).Set(float64(replicas))
}

// ClearRecommendedReplicas removes every recommendation series for the
// autoscaler. A series left behind after the CR is deleted, switched out of
// advisor mode or put in dry-run would keep pinning the consuming HPA's floor.
func ClearRecommendedReplicas(autoscalerNS, autoscaler string) {
	recommendedReplicas.DeletePartialMatch(prometheus.Labels{
		"autoscaler_namespace": autoscalerNS,
		"autoscaler":           autoscaler,
	})
}
