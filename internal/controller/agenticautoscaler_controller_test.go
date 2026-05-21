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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/observability"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/policy"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/scaler"
)

var _ = Describe("AgenticAutoscaler Controller (envtest)", func() {
	const (
		deployName = "payment-service"
		ns         = "default"
		aaName     = "payment-service-autoscaler"
		timeout    = 10 * time.Second
		interval   = 250 * time.Millisecond
	)

	ctx := context.Background()

	// reconciler wired with fakes so no live Prometheus or AI API is needed.
	var rec *AgenticAutoscalerReconciler

	BeforeEach(func() {
		rec = &AgenticAutoscalerReconciler{
			Client:          k8sClient,
			Scheme:          k8sClient.Scheme(),
			SignalCollector:  &staticSignalCollector{},
			Policy:          policy.NewEnforcer(&noopHPAReader{}),
			Scaler:          scaler.NewExecutor(k8sClient),
			Observability:   observability.NewNoopRecorder(),
			NewAgent: func(_ reasoning.Config) reasoning.Agent {
				return &fixedAgent{decision: reasoning.HoldDecision("envtest: rule-based hold")}
			},
		}
	})

	Context("when a Deployment and AgenticAutoscaler CR exist", func() {
		var deploy *appsv1.Deployment
		var aa *scalingv1alpha1.AgenticAutoscaler

		BeforeEach(func() {
			replicas := int32(3)
			deploy = &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: ns},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": deployName}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": deployName}},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "app",
								Image: "nginx:latest",
							}},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, deploy)
			Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue())

			aa = &scalingv1alpha1.AgenticAutoscaler{
				ObjectMeta: metav1.ObjectMeta{Name: aaName, Namespace: ns},
				Spec: scalingv1alpha1.AgenticAutoscalerSpec{
					TargetDeployment: deployName,
					Namespace:        ns,
					PrometheusURL:    "http://prometheus.monitoring.svc:9090",
					LogSource:        scalingv1alpha1.LogSourceConfig{Type: "loki"},
					MinReplicas:      2,
					MaxReplicas:      20,
					CooldownSeconds:  60,
					DryRun:           true,
					// Provide valid values for all enum/minLength fields so CRD
					// admission doesn't reject the CR.
					HPACoexistence: scalingv1alpha1.HPACoexistence{Mode: "owner"},
					AIProvider:     scalingv1alpha1.AIProviderConfig{Provider: "anthropic"},
					Observability: scalingv1alpha1.ObservabilityConfig{
						GrafanaURL: "http://grafana.monitoring.svc:3000",
						SecretRef:  "grafana-token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, aa)).To(Succeed())
		})

		AfterEach(func() {
			// Use IgnoreNotFound so cleanup succeeds even if BeforeEach failed
			// partway through and left the resources absent.
			_ = k8sClient.Delete(ctx, aa)
			_ = k8sClient.Delete(ctx, deploy)
		})

		It("reconciles without panic and updates status", func() {
			By("calling Reconcile once")
			result, err := rec.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: aaName, Namespace: ns},
			})

			By("checking no error returned")
			Expect(err).NotTo(HaveOccurred())

			By("checking RequeueAfter >= 30s (cooldown minimum)")
			Expect(result.RequeueAfter).To(BeNumerically(">=", 30*time.Second))

			By("verifying status.lastDecisionReason is populated")
			updated := &scalingv1alpha1.AgenticAutoscaler{}
			Eventually(func() string {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: aaName, Namespace: ns}, updated)
				return updated.Status.LastDecisionReason
			}, timeout, interval).ShouldNot(BeEmpty())

			By("verifying status.currentReplicas reflects the Deployment")
			Expect(updated.Status.CurrentReplicas).To(BeNumerically(">", 0))

			By("verifying status.observedGeneration is set")
			Expect(updated.Status.ObservedGeneration).To(Equal(aa.Generation))
		})

		It("respects DryRun — Deployment replicas unchanged after scale-up decision", func() {
			// Override agent to return a scale-up decision.
			rec.NewAgent = func(_ reasoning.Config) reasoning.Agent {
				ts := metav1.NewTime(time.Now())
				return &fixedAgent{decision: reasoning.ScaleDecision{
					Action:         "scale-up",
					TargetReplicas: 10,
					Confidence:     0.9,
					Reason:         "simulated high load",
					Timestamp:      &ts,
				}}
			}

			_, err := rec.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: aaName, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			// Deployment replicas must remain unchanged (DryRun=true).
			unchanged := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: ns}, unchanged)).To(Succeed())
			Expect(*unchanged.Spec.Replicas).To(Equal(int32(3)))

			// But the decision reason must be recorded.
			updated := &scalingv1alpha1.AgenticAutoscaler{}
			Eventually(func() string {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: aaName, Namespace: ns}, updated)
				return updated.Status.LastDecisionReason
			}, timeout, interval).ShouldNot(BeEmpty())
		})

		It("requeues at cooldownSeconds after reconcile", func() {
			result, err := rec.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: aaName, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())
			// cooldownSeconds=60 → RequeueAfter should be 60s.
			Expect(result.RequeueAfter).To(Equal(60 * time.Second))
		})
	})

	Context("when the AgenticAutoscaler CR does not exist", func() {
		It("returns nil error and empty result", func() {
			result, err := rec.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse())
			Expect(result.RequeueAfter).To(BeZero())
		})
	})
})
