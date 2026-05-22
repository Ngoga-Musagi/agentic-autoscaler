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

package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

const (
	auditLogCMName = "decision-audit-log"
	auditLogKey    = "decisions.json"
	maxEntries     = 200
)

// ConfigMapRecorder appends every DecisionRecord to a ConfigMap in the
// operator's own namespace and rotates the log to at most maxEntries entries.
//
// The namespace is read from the OPERATOR_NAMESPACE environment variable; it
// defaults to "default" when the variable is unset (useful in local runs).
type ConfigMapRecorder struct {
	client    client.Client
	namespace string
}

// NewConfigMapRecorder returns a ConfigMapRecorder that writes to the given
// client. The operator namespace is resolved from the environment at
// construction time.
func NewConfigMapRecorder(c client.Client) *ConfigMapRecorder {
	ns := os.Getenv("OPERATOR_NAMESPACE")
	if ns == "" {
		ns = "default"
	}
	return &ConfigMapRecorder{client: c, namespace: ns}
}

// newConfigMapRecorderForNamespace is used by tests to pin the namespace
// without setting an environment variable.
func newConfigMapRecorderForNamespace(c client.Client, ns string) *ConfigMapRecorder {
	return &ConfigMapRecorder{client: c, namespace: ns}
}

// Record appends rec to the ConfigMap audit log, creating the ConfigMap if it
// does not exist, and rotates the entries to keep at most maxEntries.
// It also emits a structured log line at Info level so Loki captures every
// decision in the pod log stream — making them queryable without parsing the
// ConfigMap.
func (r *ConfigMapRecorder) Record(
	ctx context.Context,
	aa scalingv1alpha1.AgenticAutoscaler,
	decision reasoning.ScaleDecision,
) error {
	rec := NewDecisionRecord(aa, decision)

	// Structured log line picked up by Loki via pod log scraping.
	// LogQL: {app="agentic-autoscaler"} | json | action != "hold"
	ctrllog.FromContext(ctx).Info("scale_decision",
		"action", rec.Action,
		"deployment", rec.Deployment,
		"namespace", rec.Namespace,
		"oldReplicas", rec.OldReplicas,
		"newReplicas", rec.NewReplicas,
		"reason", rec.Reason,
		"confidence", rec.Confidence,
		"provider", rec.Provider,
		"patterns", rec.PatternsMatched,
		"dryRun", rec.DryRun,
		"timestamp", rec.Timestamp,
	)

	cm := &corev1.ConfigMap{}
	namespacedName := types.NamespacedName{Name: auditLogCMName, Namespace: r.namespace}

	err := r.client.Get(ctx, namespacedName, cm)
	if apierrors.IsNotFound(err) {
		return r.createWithRecord(ctx, rec)
	}
	if err != nil {
		return fmt.Errorf("get audit configmap %s/%s: %w", r.namespace, auditLogCMName, err)
	}
	return r.appendAndPatch(ctx, cm, rec)
}

// createWithRecord creates the ConfigMap containing a single entry.
func (r *ConfigMapRecorder) createWithRecord(ctx context.Context, rec DecisionRecord) error {
	data, err := marshalEntries([]DecisionRecord{rec})
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      auditLogCMName,
			Namespace: r.namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "agentic-autoscaler"},
		},
		Data: map[string]string{auditLogKey: data},
	}
	if err := r.client.Create(ctx, cm); err != nil {
		return fmt.Errorf("create audit configmap: %w", err)
	}
	return nil
}

// appendAndPatch reads the current entries from the ConfigMap, appends rec,
// rotates to maxEntries, and writes back using a merge patch.
func (r *ConfigMapRecorder) appendAndPatch(
	ctx context.Context,
	cm *corev1.ConfigMap,
	rec DecisionRecord,
) error {
	entries, err := unmarshalEntries(cm.Data[auditLogKey])
	if err != nil {
		// Corrupted data: start fresh rather than refusing all future records.
		entries = nil
	}

	entries = append(entries, rec)
	if len(entries) > maxEntries {
		entries = entries[len(entries)-maxEntries:]
	}

	data, err := marshalEntries(entries)
	if err != nil {
		return err
	}

	patch := client.MergeFrom(cm.DeepCopy())
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[auditLogKey] = data
	if err := r.client.Patch(ctx, cm, patch); err != nil {
		return fmt.Errorf("patch audit configmap: %w", err)
	}
	return nil
}

func marshalEntries(entries []DecisionRecord) (string, error) {
	b, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("marshal audit entries: %w", err)
	}
	return string(b), nil
}

func unmarshalEntries(raw string) ([]DecisionRecord, error) {
	if raw == "" {
		return nil, nil
	}
	var entries []DecisionRecord
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("unmarshal audit entries: %w", err)
	}
	return entries, nil
}

// ReadAuditLog fetches all DecisionRecords stored in the operator audit
// ConfigMap. It is safe to call from other packages (e.g. the query API).
func ReadAuditLog(ctx context.Context, c client.Client, namespace string) ([]DecisionRecord, error) {
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      auditLogCMName,
		Namespace: namespace,
	}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get audit configmap: %w", err)
	}
	return unmarshalEntries(cm.Data[auditLogKey])
}
