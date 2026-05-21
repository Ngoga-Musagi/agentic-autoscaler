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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
)

var _ = Describe("AgenticAutoscaler integration", func() {
	ctx := context.Background()

	// Scenario: operator in dry-run mode sees connection pool exhaustion and
	// elevated latency, decides to scale up, but does NOT patch the Deployment
	// because dryRun=true. The reason recorded in status must carry the
	// "[dry-run]" prefix so on-call SREs know the decision was intentionally
	// suppressed.
	Context("dry-run mode", func() {
		It("records dry-run reason without changing replica count", func() {
			const ns = "int-dryrun"

			By("setting fake signal state: connection pool exhausted, latency 2500ms")
			testMetrics.set(2.5 /*latency_sec*/, 0, 50, 100)
			testLines.set([]string{"connection pool exhausted: max connections reached"})

			By("creating cluster resources")
			intCreateNS(ctx, ns)
			intMakeDeploy(ctx, "api", ns, 3)

			aa := intMakeAA("autoscaler", ns, "api", true /*dryRun*/, 2, 10, 30, "owner", "")
			Expect(k8sClient.Create(ctx, aa)).To(Succeed())

			By("running the reconciler")
			rec := intMakeRec()
			_, err := rec.Reconcile(ctx, intReq(ns, "autoscaler"))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Deployment replica count is unchanged")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "api", Namespace: ns}, deploy)).To(Succeed())
			Expect(*deploy.Spec.Replicas).To(Equal(int32(3)))

			By("verifying status carries the [dry-run] prefix")
			got := &scalingv1alpha1.AgenticAutoscaler{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "autoscaler", Namespace: ns}, got)).To(Succeed())
			Expect(got.Status.LastDecisionReason).To(ContainSubstring("[dry-run]"))
		})
	})

	// Scenario: live mode, calibrated HPA coexistence. Rule 2 of the rule-based
	// agent fires (connection-pool-exhausted + latency > 2s) and computes
	// scaleUpByFraction(3, 0.50, 15) = 5. Policy confirms the target sits within
	// both CR bounds [2, 15] and HPA bounds [2, 20]. The Deployment is patched
	// and status is updated.
	Context("live scale-up via log pattern", func() {
		It("scales the deployment and updates status within HPA bounds", func() {
			const ns = "int-scaleup"

			By("setting fake signal state: connection pool exhausted, latency 2500ms")
			testMetrics.set(2.5, 0, 50, 100)
			testLines.set([]string{"connection pool exhausted: max connections reached"})

			By("creating cluster resources (including HPA for calibrated mode)")
			intCreateNS(ctx, ns)
			intMakeDeploy(ctx, "api", ns, 3)
			intMakeHPA(ctx, "api-hpa", ns, "api", 2, 20)

			aa := intMakeAA("autoscaler", ns, "api", false, 2, 15, 0, "calibrated", "api-hpa")
			Expect(k8sClient.Create(ctx, aa)).To(Succeed())

			By("running the reconciler")
			rec := intMakeRec()
			_, err := rec.Reconcile(ctx, intReq(ns, "autoscaler"))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Deployment scaled from 3 to 5")
			// scaleUpByFraction(3, 0.50, 15): add=⌈1.5⌉=2, target=3+2=5
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "api", Namespace: ns}, deploy)).To(Succeed())
			Expect(*deploy.Spec.Replicas).To(Equal(int32(5)))

			By("verifying status.currentReplicas reflects the new count")
			got := &scalingv1alpha1.AgenticAutoscaler{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "autoscaler", Namespace: ns}, got)).To(Succeed())
			Expect(got.Status.CurrentReplicas).To(Equal(int32(5)))
		})
	})

	// Scenario: the first reconcile causes a real scale action which stamps
	// status.lastScaleTime. The second reconcile runs immediately within the
	// 60-second cooldown window. The policy enforcer converts the scale-up
	// decision to hold, leaving replica count unchanged, and the status reason
	// reflects "cooldown".
	Context("cooldown enforcement", func() {
		It("blocks a second scale within the cooldown window", func() {
			const ns = "int-cooldown"

			By("setting fake signal state: connection pool exhausted, latency 2500ms")
			testMetrics.set(2.5, 0, 50, 100)
			testLines.set([]string{"connection pool exhausted: max connections reached"})

			By("creating cluster resources with 60-second cooldown")
			intCreateNS(ctx, ns)
			intMakeDeploy(ctx, "api", ns, 3)

			aa := intMakeAA("autoscaler", ns, "api", false, 2, 10, 60, "owner", "")
			Expect(k8sClient.Create(ctx, aa)).To(Succeed())

			rec := intMakeRec()

			By("first reconcile: rule 2 fires, scales 3→5, stamps lastScaleTime")
			_, err := rec.Reconcile(ctx, intReq(ns, "autoscaler"))
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "api", Namespace: ns}, deploy)).To(Succeed())
			Expect(*deploy.Spec.Replicas).To(Equal(int32(5)))

			By("second reconcile: cooldown active, decision becomes hold")
			_, err = rec.Reconcile(ctx, intReq(ns, "autoscaler"))
			Expect(err).NotTo(HaveOccurred())

			By("verifying replica count is unchanged at 5")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "api", Namespace: ns}, deploy)).To(Succeed())
			Expect(*deploy.Spec.Replicas).To(Equal(int32(5)))

			By("verifying status reason mentions cooldown")
			got := &scalingv1alpha1.AgenticAutoscaler{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "autoscaler", Namespace: ns}, got)).To(Succeed())
			Expect(got.Status.LastDecisionReason).To(ContainSubstring("cooldown"))
		})
	})

	// Scenario: spec.maxReplicas=8 is the binding constraint, not HPA max=10.
	// Rule 2 computes scaleUpByFraction(6, 0.50, 8) = 8 (clamped by spec max
	// before the HPA bounds check). The Deployment must be patched to 8, not 10.
	Context("HPA bounds: spec.maxReplicas clamps before HPA max", func() {
		It("scales to spec max (8), not HPA max (10)", func() {
			const ns = "int-hpabounds"

			By("setting fake signal state: connection pool exhausted, latency 2500ms")
			testMetrics.set(2.5, 0, 50, 100)
			testLines.set([]string{"connection pool exhausted: max connections reached"})

			By("creating cluster resources with spec max=8 and HPA max=10")
			intCreateNS(ctx, ns)
			intMakeDeploy(ctx, "api", ns, 6)
			intMakeHPA(ctx, "api-hpa", ns, "api", 2, 10)

			// spec.maxReplicas=8; HPA.maxReplicas=10 — spec is more restrictive
			aa := intMakeAA("autoscaler", ns, "api", false, 2, 8, 0, "calibrated", "api-hpa")
			Expect(k8sClient.Create(ctx, aa)).To(Succeed())

			By("running the reconciler")
			rec := intMakeRec()
			_, err := rec.Reconcile(ctx, intReq(ns, "autoscaler"))
			Expect(err).NotTo(HaveOccurred())

			By("verifying Deployment replicas = spec max (8), not HPA max (10)")
			// scaleUpByFraction(6, 0.50, 8): add=⌈3⌉=3, target=9 → clamped to 8
			// clampToHPABounds: 8 ≤ HPA max 10 → no further restriction
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "api", Namespace: ns}, deploy)).To(Succeed())
			Expect(*deploy.Spec.Replicas).To(Equal(int32(8)))
			Expect(*deploy.Spec.Replicas).NotTo(Equal(int32(10)))

			By("verifying status.currentReplicas reflects 8")
			got := &scalingv1alpha1.AgenticAutoscaler{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "autoscaler", Namespace: ns}, got)).To(Succeed())
			Expect(got.Status.CurrentReplicas).To(Equal(int32(8)))
		})
	})
})
