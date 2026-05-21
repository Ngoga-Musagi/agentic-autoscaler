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
	"fmt"
	"time"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

// SignalCollector assembles a SystemSnapshot from the Prometheus and log
// sources configured in an AgenticAutoscaler spec.
type SignalCollector interface {
	Collect(ctx context.Context, spec scalingv1alpha1.AgenticAutoscalerSpec) (signals.SystemSnapshot, error)
}

// DefaultSignalCollector creates PrometheusCollector and LokiReader clients
// from the spec on every call. Constructing them per-call is intentional:
// each CR can point at a different Prometheus or Loki instance.
type DefaultSignalCollector struct{}

// NewDefaultSignalCollector returns a DefaultSignalCollector.
func NewDefaultSignalCollector() *DefaultSignalCollector { return &DefaultSignalCollector{} }

// Collect runs Prometheus queries and (when configured) fetches Loki log
// entries for the last 2 minutes, then assembles a SystemSnapshot.
func (d *DefaultSignalCollector) Collect(
	ctx context.Context,
	spec scalingv1alpha1.AgenticAutoscalerSpec,
) (signals.SystemSnapshot, error) {
	collector := signals.NewPrometheusCollector(spec.PrometheusURL)
	metrics, err := collector.Collect(ctx, spec.TargetDeployment)
	if err != nil {
		return signals.SystemSnapshot{}, fmt.Errorf("prometheus: %w", err)
	}

	var logEntries []signals.LogEntry
	if spec.LogSource.Type == "loki" && spec.LogSource.Loki != nil {
		reader := signals.NewLokiReader(spec.LogSource.Loki.URL, spec.LogSource.Loki.Query)
		logEntries, err = reader.Read(ctx, 2) // 2-minute lookback window
		if err != nil {
			return signals.SystemSnapshot{}, fmt.Errorf("loki: %w", err)
		}
	}

	return signals.SystemSnapshot{
		Timestamp:  time.Now(),
		Metrics:    metrics,
		LogEntries: logEntries,
	}, nil
}
