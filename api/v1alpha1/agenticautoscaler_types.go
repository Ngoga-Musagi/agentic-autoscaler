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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LogSourceConfig defines the log collection backend.
type LogSourceConfig struct {
	// Type is the log source type; must be "loki" or "kafka".
	// +kubebuilder:validation:Enum=loki;kafka
	Type string `json:"type"`

	// Loki holds configuration when Type is "loki".
	// +optional
	Loki *LokiConfig `json:"loki,omitempty"`

	// Kafka holds configuration when Type is "kafka".
	// +optional
	Kafka *KafkaConfig `json:"kafka,omitempty"`
}

// LokiConfig holds connection details for a Loki log source.
type LokiConfig struct {
	// URL is the base URL of the Loki HTTP API.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Query is the LogQL query used to fetch relevant log lines.
	// +kubebuilder:validation:MinLength=1
	Query string `json:"query"`
}

// KafkaConfig holds connection details for a Kafka log source.
type KafkaConfig struct {
	// Brokers is the list of Kafka broker addresses.
	// +kubebuilder:validation:MinItems=1
	Brokers []string `json:"brokers"`

	// Topic is the Kafka topic to consume.
	// +kubebuilder:validation:MinLength=1
	Topic string `json:"topic"`

	// GroupID is the consumer group ID.
	// +kubebuilder:validation:MinLength=1
	GroupID string `json:"groupID"`
}

// AIProviderConfig selects the AI reasoning backend.
type AIProviderConfig struct {
	// Provider is the AI backend: "anthropic", "openai", or "ollama".
	// +kubebuilder:validation:Enum=anthropic;openai;ollama
	Provider string `json:"provider"`

	// SecretRef is the name of the Kubernetes Secret holding the API key.
	// Not required when Provider is "ollama".
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// HPACoexistence controls conflict avoidance when an HPA also targets this Deployment.
type HPACoexistence struct {
	// Mode is either "owner" (agentic autoscaler is sole controller) or
	// "calibrated" (operates inside the HPA's replica envelope as a safety net).
	// +kubebuilder:validation:Enum=owner;calibrated
	Mode string `json:"mode"`

	// HPAName is the name of the HorizontalPodAutoscaler to coexist with.
	// Required when Mode is "calibrated".
	// +optional
	HPAName string `json:"hpaName,omitempty"`

	// HPANamespace is the namespace of the HPA. Defaults to the CR namespace.
	// +optional
	HPANamespace string `json:"hpaNamespace,omitempty"`
}

// ObservabilityConfig holds Grafana annotation settings for decision audit trails.
type ObservabilityConfig struct {
	// GrafanaURL is the base URL of the Grafana instance.
	// +kubebuilder:validation:MinLength=1
	GrafanaURL string `json:"grafanaURL"`

	// SecretRef is the name of the Kubernetes Secret holding the Grafana API token.
	// +kubebuilder:validation:MinLength=1
	SecretRef string `json:"secretRef"`
}

// MetricQueries lets a service override the PromQL the operator runs, so any
// application can be autoscaled regardless of its metric names. Each field is a
// full PromQL expression; the literal token $TARGET is replaced with the target
// Deployment name at query time. Empty fields fall back to the operator's
// defaults, which assume standard Prometheus HTTP instrumentation
// (http_requests_total / http_request_duration_seconds_bucket).
type MetricQueries struct {
	// LatencyP99 must return p99 latency in seconds.
	// +optional
	LatencyP99 string `json:"latencyP99,omitempty"`

	// ErrorRate must return the error percentage (0-100).
	// +optional
	ErrorRate string `json:"errorRate,omitempty"`

	// CPUUtilization must return average CPU utilisation as a percentage.
	// +optional
	CPUUtilization string `json:"cpuUtilization,omitempty"`

	// RequestsPerSecond must return the request rate.
	// +optional
	RequestsPerSecond string `json:"requestsPerSecond,omitempty"`
}

// LogPatternSpec defines a custom log pattern that adopters can add to the
// operator's built-in set without forking. Each entry is compiled and merged
// with the defaults every reconcile; an entry with the same Name as a built-in
// pattern overrides it. An entry that fails to compile is skipped (never fatal)
// and reported in status.logPatternWarnings.
type LogPatternSpec struct {
	// Name is the stable identifier surfaced in decisions and Grafana
	// annotations (e.g. "tls-handshake-timeout"). Reusing a built-in name
	// overrides that pattern.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Regex is the RE2 regular expression matched against each log message.
	// +kubebuilder:validation:MinLength=1
	Regex string `json:"regex"`

	// Severity is "warning" or "critical".
	// +kubebuilder:validation:Enum=warning;critical
	Severity string `json:"severity"`

	// Score is this pattern's contribution to the fused SeverityScore, given as a
	// weight from 0 to 100 (mapped internally to 0.0–1.0). An integer is used
	// rather than a float because floats are discouraged in CRDs for
	// cross-language portability. Example: 85 contributes a 0.85 severity weight.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Score int32 `json:"score"`
}

// DetectionConfig tunes how log-pattern signals are interpreted across
// reconcile windows before they drive a scaling decision.
type DetectionConfig struct {
	// ConsecutiveWindows is the number of consecutive reconcile windows a
	// sustained log pattern (e.g. connection-pool-exhausted) must persist
	// before the sustained scale-up rules fire. The default of 1 preserves the
	// original single-window behaviour; the timeout-storm demo sets 3 to match
	// the "across 3 consecutive log windows" annotation. Emergency signals such
	// as OOMKilled are never gated by this counter.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	ConsecutiveWindows int32 `json:"consecutiveWindows,omitempty"`
}

// AgenticAutoscalerSpec defines the desired state of AgenticAutoscaler.
type AgenticAutoscalerSpec struct {
	// TargetDeployment is the name of the Deployment this CR manages.
	// +kubebuilder:validation:MinLength=1
	TargetDeployment string `json:"targetDeployment"`

	// Namespace is the namespace of the target Deployment.
	// Defaults to the namespace of this AgenticAutoscaler CR when omitted.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// PrometheusURL is the base URL of the Prometheus HTTP API.
	// +kubebuilder:validation:MinLength=1
	PrometheusURL string `json:"prometheusURL"`

	// Metrics optionally overrides the PromQL queries used to read signals, so
	// services that do not use the default metric names can still be autoscaled.
	// +optional
	Metrics MetricQueries `json:"metrics,omitempty"`

	// LogSource configures the log collection backend.
	LogSource LogSourceConfig `json:"logSource"`

	// MinReplicas is the lower bound on replica count the operator may set.
	// +kubebuilder:validation:Minimum=0
	MinReplicas int32 `json:"minReplicas"`

	// MaxReplicas is the upper bound on replica count the operator may set.
	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas"`

	// CooldownSeconds is the minimum number of seconds between consecutive scale actions.
	// +kubebuilder:validation:Minimum=0
	CooldownSeconds int32 `json:"cooldownSeconds"`

	// DryRun enables observation-only mode: decisions are logged but never applied.
	// New deployments must run with DryRun true for at least 7 days before enabling live scaling.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`

	// AIProvider configures the AI reasoning backend.
	// +optional
	AIProvider AIProviderConfig `json:"aiProvider,omitempty"`

	// HPACoexistence controls how this operator interacts with an existing HPA on the same Deployment.
	// When Mode is "calibrated", MinReplicas must be >= HPA minReplicas and MaxReplicas must be <= HPA maxReplicas.
	// +optional
	HPACoexistence HPACoexistence `json:"hpaCoexistence,omitempty"`

	// Observability configures Grafana annotation emission for decision audit trails.
	// +optional
	Observability ObservabilityConfig `json:"observability,omitempty"`

	// Detection tunes cross-window log-pattern persistence before a scale
	// decision is taken. Omitting it preserves single-window behaviour.
	// +optional
	Detection DetectionConfig `json:"detection,omitempty"`

	// LogPatterns adds custom log patterns to the operator's built-in set so
	// adopters can match their own incident strings without forking. Each entry
	// is merged with the defaults (an entry reusing a built-in name overrides
	// it); an entry that fails to compile is skipped and reported in
	// status.logPatternWarnings. Empty preserves the built-in behaviour.
	// +optional
	LogPatterns []LogPatternSpec `json:"logPatterns,omitempty"`
}

// AgenticAutoscalerStatus defines the observed state of AgenticAutoscaler.
type AgenticAutoscalerStatus struct {
	// CurrentReplicas is the replica count of the target Deployment as of the last reconcile.
	// +optional
	CurrentReplicas int32 `json:"currentReplicas,omitempty"`

	// LastScaleTime is the timestamp of the most recent live scale action.
	// +optional
	LastScaleTime *metav1.Time `json:"lastScaleTime,omitempty"`

	// LastDecisionReason is the human-readable explanation of the last scaling decision.
	// +optional
	LastDecisionReason string `json:"lastDecisionReason,omitempty"`

	// HPACoexistenceStatus reflects the current HPA coexistence mode:
	// "owner", "calibrated", or "conflict-detected".
	// +optional
	HPACoexistenceStatus string `json:"hpaCoexistenceStatus,omitempty"`

	// ObservedGeneration is the .metadata.generation this status was produced from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastCleanSince is when the most recent matched log pattern last cleared.
	// It is the durable, cross-reconcile anchor for the sustained-quiet
	// scale-down rule: the rule may fire only once the clean window has elapsed
	// since this timestamp. The controller starts it on the first pattern-free
	// reconcile and clears it whenever a pattern reappears. A nil value means
	// patterns are currently active (or were on the last reconcile).
	// +optional
	LastCleanSince *metav1.Time `json:"lastCleanSince,omitempty"`

	// ConsecutivePatternWindows counts how many consecutive reconciles have seen
	// at least one matched log pattern. It backs spec.detection.consecutiveWindows:
	// the sustained scale-up rules fire only once this counter reaches the
	// configured threshold. It resets to 0 on the first pattern-free reconcile.
	// +optional
	ConsecutivePatternWindows int32 `json:"consecutivePatternWindows,omitempty"`

	// LogPatternWarnings lists custom patterns from spec.logPatterns that were
	// skipped this reconcile because they failed to compile (e.g. an invalid
	// regex). It is empty when every custom pattern is valid or none are set.
	// The operator continues with the remaining valid patterns.
	// +optional
	LogPatternWarnings []string `json:"logPatternWarnings,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetDeployment`
//+kubebuilder:printcolumn:name="Min",type=integer,JSONPath=`.spec.minReplicas`
//+kubebuilder:printcolumn:name="Max",type=integer,JSONPath=`.spec.maxReplicas`
//+kubebuilder:printcolumn:name="Current",type=integer,JSONPath=`.status.currentReplicas`
//+kubebuilder:printcolumn:name="DryRun",type=boolean,JSONPath=`.spec.dryRun`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgenticAutoscaler is the Schema for the agenticautoscalers API.
type AgenticAutoscaler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgenticAutoscalerSpec   `json:"spec,omitempty"`
	Status AgenticAutoscalerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// AgenticAutoscalerList contains a list of AgenticAutoscaler.
type AgenticAutoscalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgenticAutoscaler `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgenticAutoscaler{}, &AgenticAutoscalerList{})
}
