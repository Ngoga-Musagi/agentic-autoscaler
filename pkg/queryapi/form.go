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

package queryapi

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
)

// Defaults applied to an onboarding request when the operator leaves a field
// blank. They mirror the in-cluster service names the bootstrap stack installs
// so the "happy path" form submits without the user filling every box.
const (
	defaultAISecretRef      = "ai-provider-secret"
	defaultGrafanaURL       = "http://kube-prometheus-stack-grafana.monitoring"
	defaultGrafanaSecretRef = "grafana-api-secret"
	defaultLogSourceType    = "loki"
	defaultProvider         = "anthropic"
	defaultHPAMode          = "owner"
)

// OnboardRequest is the JSON body for POST /api/autoscalers. It wraps the CRD
// spec verbatim so the browser form maps one-to-one onto AgenticAutoscalerSpec,
// plus the object metadata the user chooses (name + namespace).
type OnboardRequest struct {
	// Name is the AgenticAutoscaler object name. Defaults to
	// "<targetDeployment>-autoscaler" when empty.
	Name string `json:"name,omitempty"`
	// Namespace is where the AgenticAutoscaler CR is created. Defaults to the
	// target Deployment's namespace.
	Namespace string `json:"namespace"`
	// Spec is the desired AgenticAutoscaler spec, identical to the CRD.
	Spec scalingv1alpha1.AgenticAutoscalerSpec `json:"spec"`
}

// Normalize fills blank fields with safe defaults so a minimally-completed form
// still produces a CRD-valid object. It never overrides a value the user set.
//
// The value-type sub-structs (aiProvider, hpaCoexistence, observability) always
// serialize even when empty, so each must carry CRD-valid content or the API
// server rejects the object — Normalize guarantees that.
func (r *OnboardRequest) Normalize() {
	r.Name = strings.TrimSpace(r.Name)
	r.Namespace = strings.TrimSpace(r.Namespace)
	s := &r.Spec

	if r.Namespace == "" {
		r.Namespace = s.Namespace
	}
	if s.Namespace == "" {
		s.Namespace = r.Namespace
	}
	if r.Name == "" && s.TargetDeployment != "" {
		r.Name = s.TargetDeployment + "-autoscaler"
	}

	if s.LogSource.Type == "" {
		s.LogSource.Type = defaultLogSourceType
	}
	if s.AIProvider.Provider == "" {
		s.AIProvider.Provider = defaultProvider
	}
	if s.AIProvider.Provider != "ollama" && s.AIProvider.SecretRef == "" {
		s.AIProvider.SecretRef = defaultAISecretRef
	}
	if s.HPACoexistence.Mode == "" {
		s.HPACoexistence.Mode = defaultHPAMode
	}
	if s.Observability.GrafanaURL == "" {
		s.Observability.GrafanaURL = defaultGrafanaURL
	}
	if s.Observability.SecretRef == "" {
		s.Observability.SecretRef = defaultGrafanaSecretRef
	}
}

// Validate runs the pure (cluster-independent) checks that mirror the CRD
// schema and the project's safety rules. Cluster-dependent checks — that the
// target Deployment exists and that calibrated bounds nest inside the HPA — run
// in the handler against a live client.
func (r *OnboardRequest) Validate() error {
	s := r.Spec
	if strings.TrimSpace(s.TargetDeployment) == "" {
		return fmt.Errorf("targetDeployment is required")
	}
	if strings.TrimSpace(r.Namespace) == "" {
		return fmt.Errorf("namespace is required")
	}
	if strings.TrimSpace(s.PrometheusURL) == "" {
		return fmt.Errorf("prometheusURL is required")
	}
	if s.MaxReplicas < 1 {
		return fmt.Errorf("maxReplicas must be at least 1")
	}
	if s.MinReplicas < 0 {
		return fmt.Errorf("minReplicas must not be negative")
	}
	if s.MinReplicas > s.MaxReplicas {
		return fmt.Errorf("minReplicas must not exceed maxReplicas")
	}
	if s.CooldownSeconds < 0 {
		return fmt.Errorf("cooldownSeconds must not be negative")
	}

	switch s.AIProvider.Provider {
	case "anthropic", "openai", "ollama":
	default:
		return fmt.Errorf("aiProvider.provider must be one of anthropic, openai, ollama")
	}

	if err := validateLogSource(s.LogSource); err != nil {
		return err
	}

	switch s.HPACoexistence.Mode {
	case "owner":
	case "calibrated":
		if strings.TrimSpace(s.HPACoexistence.HPAName) == "" {
			return fmt.Errorf("hpaCoexistence.hpaName is required in calibrated mode")
		}
	default:
		return fmt.Errorf("hpaCoexistence.mode must be owner or calibrated")
	}

	if strings.TrimSpace(s.Observability.GrafanaURL) == "" {
		return fmt.Errorf("observability.grafanaURL is required")
	}
	if strings.TrimSpace(s.Observability.SecretRef) == "" {
		return fmt.Errorf("observability.secretRef is required")
	}
	return nil
}

func validateLogSource(ls scalingv1alpha1.LogSourceConfig) error {
	switch ls.Type {
	case "loki":
		if ls.Loki == nil || strings.TrimSpace(ls.Loki.URL) == "" || strings.TrimSpace(ls.Loki.Query) == "" {
			return fmt.Errorf("logSource.loki requires url and query")
		}
	case "kafka":
		if ls.Kafka == nil || len(ls.Kafka.Brokers) == 0 ||
			strings.TrimSpace(ls.Kafka.Topic) == "" || strings.TrimSpace(ls.Kafka.GroupID) == "" {
			return fmt.Errorf("logSource.kafka requires brokers, topic and groupID")
		}
	default:
		return fmt.Errorf("logSource.type must be loki or kafka")
	}
	return nil
}

// ToCR builds the AgenticAutoscaler object that will be applied to the cluster.
// Call Normalize and Validate first.
func (r *OnboardRequest) ToCR() *scalingv1alpha1.AgenticAutoscaler {
	return &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.Name,
			Namespace: r.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "agentic-autoscaler-console",
			},
		},
		Spec: r.Spec,
	}
}
