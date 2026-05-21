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

// Package observability emits scale decisions to Grafana annotations and an
// in-cluster audit ConfigMap so on-call SREs can inspect the operator's
// reasoning without reading raw logs.
package observability

import (
	"context"
	"time"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/reasoning"
)

// DecisionRecord is a snapshot of one reconcile cycle's outcome, persisted to
// the audit log so on-call SREs can trace why each scale action happened.
type DecisionRecord struct {
	Timestamp       time.Time `json:"timestamp"`
	Deployment      string    `json:"deployment"`
	Namespace       string    `json:"namespace"`
	OldReplicas     int32     `json:"oldReplicas"`
	NewReplicas     int32     `json:"newReplicas"`
	Action          string    `json:"action"`
	Reason          string    `json:"reason"`
	Confidence      float64   `json:"confidence"`
	Provider        string    `json:"provider"`
	PatternsMatched []string  `json:"patternsMatched,omitempty"`
	DryRun          bool      `json:"dryRun"`
}

// NewDecisionRecord constructs a DecisionRecord from the live AgenticAutoscaler
// object and the ScaleDecision returned by the reasoning engine.
func NewDecisionRecord(aa scalingv1alpha1.AgenticAutoscaler, d reasoning.ScaleDecision) DecisionRecord {
	ts := time.Now()
	if d.Timestamp != nil {
		ts = d.Timestamp.Time
	}
	ns := aa.Spec.Namespace
	if ns == "" {
		ns = aa.Namespace
	}
	newReplicas := d.OldReplicas // unchanged for hold
	if d.Action != "hold" {
		newReplicas = d.TargetReplicas
	}
	return DecisionRecord{
		Timestamp:       ts,
		Deployment:      aa.Spec.TargetDeployment,
		Namespace:       ns,
		OldReplicas:     d.OldReplicas,
		NewReplicas:     newReplicas,
		Action:          d.Action,
		Reason:          d.Reason,
		Confidence:      d.Confidence,
		Provider:        d.Provider,
		PatternsMatched: d.PatternsMatched,
		DryRun:          aa.Spec.DryRun,
	}
}

// Recorder records each scale decision for observability purposes.
// Implementations must treat failures as non-fatal — the Reconcile loop logs
// the error but never stops because of an observability failure.
type Recorder interface {
	Record(ctx context.Context, aa scalingv1alpha1.AgenticAutoscaler, decision reasoning.ScaleDecision) error
}

// NoopRecorder satisfies Recorder with a no-op. Useful in unit tests and as
// a compile-time check that the interface is implemented.
type NoopRecorder struct{}

// NewNoopRecorder returns a NoopRecorder.
func NewNoopRecorder() *NoopRecorder { return &NoopRecorder{} }

// Record is a no-op.
func (n *NoopRecorder) Record(_ context.Context, _ scalingv1alpha1.AgenticAutoscaler, _ reasoning.ScaleDecision) error {
	return nil
}
