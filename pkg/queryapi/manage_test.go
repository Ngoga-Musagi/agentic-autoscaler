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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
)

func i32(i int32) *int32 { return &i }

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := scalingv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newTestServer(t *testing.T, cfg ConsoleConfig, objs ...client.Object) (*Server, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&scalingv1alpha1.AgenticAutoscaler{}).
		Build()
	return &Server{k8sClient: c, namespace: "default", cfg: cfg}, c
}

func deployment(ns, name string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(replicas)},
	}
}

func doJSON(t *testing.T, h http.HandlerFunc, method, path, token string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestHandleConfig(t *testing.T) {
	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true, AuthToken: "secret", LoadgenURL: "http://lg"})
	rec := doJSON(t, s.handleConfig, http.MethodGet, "/api/config", "", nil)
	var got consoleConfigResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.WriteEnabled || !got.AuthRequired || !got.LoadgenEnabled {
		t.Errorf("config response wrong: %+v", got)
	}
}

func TestDeploymentsManagedFlag(t *testing.T) {
	managedAA := &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "production", Name: "pay-as"},
		Spec: scalingv1alpha1.AgenticAutoscalerSpec{
			TargetDeployment: "payment-service", Namespace: "production",
		},
	}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "production", Name: "orders-hpa"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "orders"},
			MaxReplicas:    5,
		},
	}
	s, _ := newTestServer(t, ConsoleConfig{},
		deployment("production", "payment-service", 3),
		deployment("production", "orders", 1),
		managedAA, hpa)

	rec := doJSON(t, s.handleDeployments, http.MethodGet, "/api/deployments?namespace=production", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got []deploymentInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	byName := map[string]deploymentInfo{}
	for _, d := range got {
		byName[d.Name] = d
	}
	if !byName["payment-service"].Managed {
		t.Errorf("payment-service should be managed")
	}
	if byName["orders"].Managed {
		t.Errorf("orders should not be managed")
	}
	if !byName["orders"].HasHPA {
		t.Errorf("orders should be flagged as having an HPA")
	}
}

func TestCreateAutoscalerWriteDisabled(t *testing.T) {
	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: false}, deployment("production", "payment-service", 3))
	rec := doJSON(t, s.handleAutoscalers, http.MethodPost, "/api/autoscalers", "", validReq())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAutoscalerBadToken(t *testing.T) {
	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true, AuthToken: "right"}, deployment("production", "payment-service", 3))
	rec := doJSON(t, s.handleAutoscalers, http.MethodPost, "/api/autoscalers", "wrong", validReq())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAutoscalerHappyPath(t *testing.T) {
	s, c := newTestServer(t, ConsoleConfig{WriteEnabled: true, AuthToken: "tok"}, deployment("production", "payment-service", 3))
	rec := doJSON(t, s.handleAutoscalers, http.MethodPost, "/api/autoscalers", "tok", validReq())
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var cr scalingv1alpha1.AgenticAutoscaler
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "production", Name: "payment-service-autoscaler"}, &cr); err != nil {
		t.Fatalf("CR not created: %v", err)
	}
	if cr.Spec.AIProvider.Provider != "anthropic" || cr.Spec.Observability.GrafanaURL == "" {
		t.Errorf("defaults not applied on create: %+v", cr.Spec)
	}
}

func TestCreateAutoscalerMissingDeployment(t *testing.T) {
	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true})
	rec := doJSON(t, s.handleAutoscalers, http.MethodPost, "/api/autoscalers", "", validReq())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateCalibratedExceedsHPA(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "production", Name: "pay-hpa"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "payment-service"},
			MinReplicas:    i32(3), MaxReplicas: 8,
		},
	}
	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true}, deployment("production", "payment-service", 3), hpa)

	req := validReq()
	req.Spec.MinReplicas = 3  // satisfies HPA min of 3
	req.Spec.MaxReplicas = 20 // exceeds HPA max of 8
	req.Spec.HPACoexistence = scalingv1alpha1.HPACoexistence{Mode: "calibrated", HPAName: "pay-hpa"}

	rec := doJSON(t, s.handleAutoscalers, http.MethodPost, "/api/autoscalers", "", req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exceeds hpa maxReplicas") {
		t.Errorf("unexpected error: %s", rec.Body.String())
	}
}

func TestPatchToggleDryRun(t *testing.T) {
	aa := &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "production", Name: "pay-as"},
		Spec: scalingv1alpha1.AgenticAutoscalerSpec{
			TargetDeployment: "payment-service", PrometheusURL: "http://p", MinReplicas: 2, MaxReplicas: 10,
			DryRun: true,
		},
	}
	s, c := newTestServer(t, ConsoleConfig{WriteEnabled: true}, aa)
	rec := doJSON(t, s.handleAutoscalerItem, http.MethodPatch, "/api/autoscalers/production/pay-as", "", map[string]bool{"dryRun": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got scalingv1alpha1.AgenticAutoscaler
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "production", Name: "pay-as"}, &got)
	if got.Spec.DryRun {
		t.Errorf("dryRun should be false after patch")
	}
}

func TestDeleteAutoscaler(t *testing.T) {
	aa := &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "production", Name: "pay-as"},
		Spec:       scalingv1alpha1.AgenticAutoscalerSpec{TargetDeployment: "payment-service", PrometheusURL: "http://p", MaxReplicas: 5},
	}
	s, c := newTestServer(t, ConsoleConfig{WriteEnabled: true}, aa)
	rec := doJSON(t, s.handleAutoscalerItem, http.MethodDelete, "/api/autoscalers/production/pay-as", "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	var got scalingv1alpha1.AgenticAutoscaler
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "production", Name: "pay-as"}, &got)
	if err == nil {
		t.Errorf("CR should be deleted")
	}
}

func TestNamespacesListed(t *testing.T) {
	s, _ := newTestServer(t, ConsoleConfig{},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "production"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})
	rec := doJSON(t, s.handleNamespaces, http.MethodGet, "/api/namespaces", "", nil)
	var got []string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 2 || got[0] != "default" {
		t.Errorf("namespaces not sorted/listed: %v", got)
	}
}

func TestLoadProxyNotConfigured(t *testing.T) {
	s, _ := newTestServer(t, ConsoleConfig{WriteEnabled: true})
	rec := doJSON(t, s.handleLoadStart, http.MethodPost, "/api/load/start", "", map[string]int{"rps": 10})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", rec.Code, rec.Body.String())
	}
}
