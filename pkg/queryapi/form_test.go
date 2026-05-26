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
	"testing"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
)

// validReq returns a minimally-complete request that passes Validate.
func validReq() OnboardRequest {
	return OnboardRequest{
		Namespace: "production",
		Spec: scalingv1alpha1.AgenticAutoscalerSpec{
			TargetDeployment: "payment-service",
			PrometheusURL:    "http://prom:9090",
			MinReplicas:      2,
			MaxReplicas:      10,
			CooldownSeconds:  60,
			LogSource: scalingv1alpha1.LogSourceConfig{
				Type: "loki",
				Loki: &scalingv1alpha1.LokiConfig{URL: "http://loki:3100", Query: `{app="x"}`},
			},
		},
	}
}

func TestNormalizeFillsCRDValidDefaults(t *testing.T) {
	r := validReq()
	r.Spec.AIProvider = scalingv1alpha1.AIProviderConfig{}
	r.Spec.HPACoexistence = scalingv1alpha1.HPACoexistence{}
	r.Spec.Observability = scalingv1alpha1.ObservabilityConfig{}
	r.Normalize()

	if r.Name != "payment-service-autoscaler" {
		t.Errorf("name default: got %q", r.Name)
	}
	if r.Spec.AIProvider.Provider != "anthropic" || r.Spec.AIProvider.SecretRef != defaultAISecretRef {
		t.Errorf("aiProvider not defaulted: %+v", r.Spec.AIProvider)
	}
	if r.Spec.HPACoexistence.Mode != "owner" {
		t.Errorf("hpa mode default: got %q", r.Spec.HPACoexistence.Mode)
	}
	if r.Spec.Observability.GrafanaURL == "" || r.Spec.Observability.SecretRef == "" {
		t.Errorf("observability must be non-empty to satisfy CRD: %+v", r.Spec.Observability)
	}
}

func TestNormalizeDoesNotOverrideOllamaSecret(t *testing.T) {
	r := validReq()
	r.Spec.AIProvider = scalingv1alpha1.AIProviderConfig{Provider: "ollama"}
	r.Normalize()
	if r.Spec.AIProvider.SecretRef != "" {
		t.Errorf("ollama should not get a secretRef, got %q", r.Spec.AIProvider.SecretRef)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*OnboardRequest)
		wantErr bool
	}{
		{"valid", func(r *OnboardRequest) { r.Normalize() }, false},
		{"missing target", func(r *OnboardRequest) { r.Spec.TargetDeployment = ""; r.Normalize() }, true},
		{"missing prometheus", func(r *OnboardRequest) { r.Spec.PrometheusURL = ""; r.Normalize() }, true},
		{"max zero", func(r *OnboardRequest) { r.Spec.MaxReplicas = 0; r.Normalize() }, true},
		{"min over max", func(r *OnboardRequest) { r.Spec.MinReplicas = 11; r.Normalize() }, true},
		{"bad provider", func(r *OnboardRequest) { r.Normalize(); r.Spec.AIProvider.Provider = "bard" }, true},
		{"loki missing query", func(r *OnboardRequest) { r.Spec.LogSource.Loki.Query = ""; r.Normalize() }, true},
		{"calibrated needs hpa name", func(r *OnboardRequest) {
			r.Spec.HPACoexistence = scalingv1alpha1.HPACoexistence{Mode: "calibrated"}
			r.Normalize()
		}, true},
		{"calibrated with hpa name", func(r *OnboardRequest) {
			r.Spec.HPACoexistence = scalingv1alpha1.HPACoexistence{Mode: "calibrated", HPAName: "x-hpa"}
			r.Normalize()
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := validReq()
			tc.mutate(&r)
			err := r.Validate()
			if tc.wantErr != (err != nil) {
				t.Errorf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestToCRSetsManagedByLabel(t *testing.T) {
	r := validReq()
	r.Normalize()
	cr := r.ToCR()
	if cr.Labels["app.kubernetes.io/managed-by"] != "agentic-autoscaler-console" {
		t.Errorf("missing managed-by label: %v", cr.Labels)
	}
	if cr.Name != "payment-service-autoscaler" || cr.Namespace != "production" {
		t.Errorf("metadata wrong: %s/%s", cr.Namespace, cr.Name)
	}
	if cr.Spec.TargetDeployment != "payment-service" {
		t.Errorf("spec not copied")
	}
}
