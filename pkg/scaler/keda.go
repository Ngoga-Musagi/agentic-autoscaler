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

package scaler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

// scaledObjectGVK is the GroupVersionKind for KEDA ScaledObjects.
// We use unstructured access to avoid importing the KEDA SDK as a dependency.
var scaledObjectGVK = schema.GroupVersionKind{
	Group:   "keda.sh",
	Version: "v1alpha1",
	Kind:    "ScaledObject",
}

// KEDAExecutor implements Scaler by creating or patching a KEDA ScaledObject
// instead of patching Deployment.spec.replicas directly. This is the execution
// path when spec.scaler.type == "keda".
//
// The ScaledObject is named <targetDeployment>-agentic and is managed in the
// same namespace as the target Deployment. KEDA then controls the replica count
// based on its configured triggers within [MinReplicaCount, MaxReplicaCount].
//
// We access KEDA resources as unstructured objects to avoid adding the KEDA
// SDK as a Go dependency — only the API surface we need is encoded here.
type KEDAExecutor struct {
	client client.Client
}

// NewKEDAExecutor returns a KEDAExecutor backed by the provided Kubernetes client.
func NewKEDAExecutor(c client.Client) *KEDAExecutor {
	return &KEDAExecutor{client: c}
}

// Execute creates or patches the KEDA ScaledObject for the target Deployment.
// On "hold" decisions it is a no-op. On scale-up or scale-down it ensures the
// ScaledObject exists with up-to-date MinReplicaCount / MaxReplicaCount bounds.
func (k *KEDAExecutor) Execute(
	ctx context.Context,
	spec v1alpha1.AgenticAutoscalerSpec,
	decision reasoning.ScaleDecision,
) error {
	if decision.Action == "hold" {
		return nil
	}

	ns := spec.Namespace
	name := spec.TargetDeployment + "-agentic"

	desiredSpec := map[string]interface{}{
		"scaleTargetRef": map[string]interface{}{
			"name": spec.TargetDeployment,
		},
		"minReplicaCount": int64(spec.MinReplicas),
		"maxReplicaCount": int64(spec.MaxReplicas),
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(scaledObjectGVK)

	err := k.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, existing)
	if apierrors.IsNotFound(err) {
		obj := buildScaledObject(name, ns, desiredSpec)
		if createErr := k.client.Create(ctx, obj); createErr != nil {
			return fmt.Errorf("create scaledobject %s/%s: %w", ns, name, createErr)
		}
		log.Printf("INFO: keda: created ScaledObject %s/%s (%s: %s)",
			ns, name, decision.Action, decision.Reason)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get scaledobject %s/%s: %w", ns, name, err)
	}

	// Patch the existing ScaledObject's spec with updated bounds.
	patchBytes, marshalErr := json.Marshal(map[string]interface{}{"spec": desiredSpec})
	if marshalErr != nil {
		return fmt.Errorf("marshal scaledobject spec: %w", marshalErr)
	}
	if patchErr := k.client.Patch(ctx, existing, client.RawPatch(types.MergePatchType, patchBytes)); patchErr != nil {
		return fmt.Errorf("patch scaledobject %s/%s: %w", ns, name, patchErr)
	}
	log.Printf("INFO: keda: patched ScaledObject %s/%s (%s: %s)",
		ns, name, decision.Action, decision.Reason)
	return nil
}

// buildScaledObject constructs the unstructured ScaledObject manifest.
func buildScaledObject(name, ns string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "keda.sh/v1alpha1",
			"kind":       "ScaledObject",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": ns,
			},
			"spec": spec,
		},
	}
}
