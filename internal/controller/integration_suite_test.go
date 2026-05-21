//go:build integration

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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/observability"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/policy"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/scaler"
)

// ---- Shared fake server state ----------------------------------------------

var (
	testMetrics = &intFakeMetrics{}
	testLines   = &intFakeLines{}
	intProm     *httptest.Server
	intLoki     *httptest.Server
)

func init() {
	intProm = httptest.NewServer(intPromHandlerFn())
	intLoki = httptest.NewServer(intLokiHandlerFn())
}

// intFakeMetrics holds per-test Prometheus metric values, safe for concurrent use.
type intFakeMetrics struct {
	mu      sync.RWMutex
	latency float64 // seconds — PrometheusCollector multiplies by 1000 to get ms
	errRate float64 // percent
	cpu     float64 // percent
	rps     float64 // requests/sec
}

func (m *intFakeMetrics) set(latency, errRate, cpu, rps float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latency = latency
	m.errRate = errRate
	m.cpu = cpu
	m.rps = rps
}

// valueFor routes the query string to the matching metric value.
// Routing mirrors the four queries issued by PrometheusCollector.Collect:
//   - "duration_seconds_bucket" → latency (histogram_quantile query)
//   - "status=~"               → error rate
//   - "container_cpu_usage"    → CPU utilisation
//   - else                     → requests per second
func (m *intFakeMetrics) valueFor(query string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch {
	case strings.Contains(query, "duration_seconds_bucket"):
		return m.latency
	case strings.Contains(query, "status=~"):
		return m.errRate
	case strings.Contains(query, "container_cpu_usage"):
		return m.cpu
	default:
		return m.rps
	}
}

// intFakeLines holds per-test Loki log lines, safe for concurrent use.
type intFakeLines struct {
	mu    sync.RWMutex
	lines []string
}

func (l *intFakeLines) set(lines []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append([]string(nil), lines...)
}

func (l *intFakeLines) get() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]string, len(l.lines))
	copy(result, l.lines)
	return result
}

// ---- Fake HTTP handlers ----------------------------------------------------

// intPromHandlerFn returns a handler that serves Prometheus instant-query
// responses routed by the "query" URL parameter.
func intPromHandlerFn() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		val := testMetrics.valueFor(query)
		resp := map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "vector",
				"result": []map[string]interface{}{
					{
						"metric": map[string]string{},
						"value":  []interface{}{float64(time.Now().Unix()), fmt.Sprintf("%g", val)},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// intLokiHandlerFn returns a handler that serves Loki query_range responses
// containing the current testLines entries as a single stream.
func intLokiHandlerFn() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lines := testLines.get()
		ts := strconv.FormatInt(time.Now().Add(-30*time.Second).UnixNano(), 10)

		var vals [][]string
		for _, line := range lines {
			vals = append(vals, []string{ts, line})
		}

		streams := []map[string]interface{}{}
		if len(vals) > 0 {
			streams = append(streams, map[string]interface{}{
				"stream": map[string]string{"app": "test"},
				"values": vals,
			})
		}
		resp := map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "streams",
				"result":     streams,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// ---- Kubernetes helpers ----------------------------------------------------

func intCreateNS(ctx context.Context, name string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
}

func intMakeDeploy(ctx context.Context, name, ns string, replicas int32) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
}

// intMakeHPA creates an HPA targeting the named Deployment. The HPA is used by
// the policy enforcer in "calibrated" coexistence mode to further clamp the
// target replica count within the HPA's configured bounds.
func intMakeHPA(ctx context.Context, name, ns, targetDeploy string, minR, maxR int32) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       targetDeploy,
			},
			MinReplicas: int32Ptr(minR),
			MaxReplicas: maxR,
		},
	}
	Expect(k8sClient.Create(ctx, hpa)).To(Succeed())
}

// intMakeAA builds an AgenticAutoscaler spec wired to the shared fake Prometheus
// and Loki servers. hpaMode must be "owner" or "calibrated"; set hpaName to the
// HPA created by intMakeHPA when using "calibrated" mode.
func intMakeAA(
	name, ns, target string,
	dryRun bool,
	minR, maxR, cooldown int32,
	hpaMode, hpaName string,
) *scalingv1alpha1.AgenticAutoscaler {
	return &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: scalingv1alpha1.AgenticAutoscalerSpec{
			TargetDeployment: target,
			Namespace:        ns,
			PrometheusURL:    intProm.URL,
			LogSource: scalingv1alpha1.LogSourceConfig{
				Type: "loki",
				Loki: &scalingv1alpha1.LokiConfig{
					URL:   intLoki.URL,
					Query: `{app="test"}`,
				},
			},
			MinReplicas:     minR,
			MaxReplicas:     maxR,
			CooldownSeconds: cooldown,
			DryRun:          dryRun,
			// Must provide valid enum values — CRD schema validates even optional structs
			// when they are present in the serialized JSON (Go encoding/json does not
			// omit zero-value structs despite the omitempty tag).
			AIProvider: scalingv1alpha1.AIProviderConfig{Provider: "anthropic"},
			HPACoexistence: scalingv1alpha1.HPACoexistence{
				Mode:         hpaMode,
				HPAName:      hpaName,
				HPANamespace: ns,
			},
			Observability: scalingv1alpha1.ObservabilityConfig{
				GrafanaURL: "http://grafana.test:3000",
				SecretRef:  "grafana-token",
			},
		},
	}
}

// intMakeRec returns a reconciler backed by the real envtest k8sClient, the
// DefaultSignalCollector (pointed at the fake Prometheus/Loki servers), the
// real policy enforcer and scaler, and NewAgentFromEnv (which falls back to
// the deterministic rule-based agent when AI_PROVIDER is not set).
func intMakeRec() *AgenticAutoscalerReconciler {
	return &AgenticAutoscalerReconciler{
		Client:          k8sClient,
		Scheme:          nil, // Scheme is only used in SetupWithManager, not in Reconcile
		SignalCollector:  NewDefaultSignalCollector(),
		Policy:          policy.NewEnforcer(&k8sHPAReader{client: k8sClient}),
		Scaler:          scaler.NewExecutor(k8sClient),
		Observability:   observability.NewNoopRecorder(),
		NewAgent:        reasoning.NewAgentFromEnv,
	}
}

func intReq(ns, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

func int32Ptr(i int32) *int32 { return &i }
