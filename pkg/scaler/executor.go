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

// Package scaler applies validated ScaleDecisions to the Kubernetes cluster.
// Executor patches Deployment.spec.replicas; KEDAExecutor manages a KEDA
// ScaledObject for deployments that use KEDA as their scaling backend.
package scaler

import (
	"context"
	"fmt"
	"log"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

// Scaler applies a ScaleDecision to the Kubernetes cluster.
// Executor (Deployment merge patch) and KEDAExecutor (ScaledObject upsert)
// both implement this interface. The controller selects the implementation
// based on spec.scaler.type.
type Scaler interface {
	Execute(ctx context.Context, spec v1alpha1.AgenticAutoscalerSpec, decision reasoning.ScaleDecision) error
}

// Executor implements Scaler by patching Deployment.spec.replicas using a
// JSON merge patch. This is the default execution path.
//
// Per kubernetes-api skill: always use strategic/merge patch — never replace
// the entire Deployment spec.
type Executor struct {
	client client.Client
}

// NewExecutor returns an Executor backed by the provided Kubernetes client.
func NewExecutor(c client.Client) *Executor {
	return &Executor{client: c}
}

// Execute patches the target Deployment's replica count to decision.TargetReplicas.
// It fetches the Deployment first to log the old replica count alongside the new
// count and the decision reason. Returns nil without patching for hold decisions.
func (e *Executor) Execute(
	ctx context.Context,
	spec v1alpha1.AgenticAutoscalerSpec,
	decision reasoning.ScaleDecision,
) error {
	if decision.Action == "hold" {
		return nil
	}

	ns := spec.Namespace
	name := spec.TargetDeployment

	// Defense in depth: an empty namespace means the Deployment lookup would
	// target namespace "" and fail confusingly. The controller resolves the
	// namespace before calling Execute; guard here so a misuse fails loudly.
	if ns == "" {
		return fmt.Errorf("scale %s: target namespace is empty", name)
	}

	// GET the Deployment to capture current replicas for the audit log.
	current := &appsv1.Deployment{}
	if err := e.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, current); err != nil {
		return fmt.Errorf("get deployment %s/%s: %w", ns, name, err)
	}

	oldReplicas := int32(0)
	if current.Spec.Replicas != nil {
		oldReplicas = *current.Spec.Replicas
	}

	// Merge patch: sets only spec.replicas — all other fields are untouched.
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, decision.TargetReplicas))
	if err := e.client.Patch(ctx, current, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("patch deployment %s/%s replicas: %w", ns, name, err)
	}

	log.Printf("INFO: scaler: %s/%s replicas %d → %d (%s: %s)",
		ns, name, oldReplicas, decision.TargetReplicas, decision.Action, decision.Reason)
	return nil
}
