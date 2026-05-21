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
