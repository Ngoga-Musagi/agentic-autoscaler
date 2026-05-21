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

// Package signals collects Prometheus metrics and log streams and assembles
// them into a SystemSnapshot for downstream fusion and reasoning.
//
// This package has zero Kubernetes dependencies — it is pure Go + HTTP.
package signals

import "time"

// SystemSnapshot is the complete picture of a deployment's health at one point
// in time, assembled from metrics and log streams before being passed to fusion.
type SystemSnapshot struct {
	Timestamp  time.Time
	Metrics    MetricSnapshot
	LogEntries []LogEntry
}

// MetricSnapshot holds the scalar metrics collected from Prometheus for a single
// reconcile cycle.
type MetricSnapshot struct {
	// LatencyP99Ms is the 99th-percentile HTTP request latency in milliseconds.
	LatencyP99Ms float64

	// ErrorRatePct is the percentage of HTTP 5xx responses over the last 2 minutes.
	ErrorRatePct float64

	// CPUUtilPct is the average CPU utilisation percentage across all pods.
	CPUUtilPct float64

	// RequestsPerSec is the incoming request rate over the last 2 minutes.
	RequestsPerSec float64

	// CustomMetrics holds any additional user-defined PromQL results keyed by name.
	CustomMetrics map[string]float64
}

// LogEntry is a single log line from Loki or Kafka, enriched with its labels.
type LogEntry struct {
	Timestamp time.Time
	Message   string
	Labels    map[string]string
}
