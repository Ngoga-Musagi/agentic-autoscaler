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

package signals

import "context"

// BacklogSource reports how many work items are queued for a workload. A
// Prometheus queue-depth gauge is the first implementation; Kafka consumer lag
// or a Redis list length can implement the same interface without touching the
// rule engine, which only sees MetricSnapshot.Backlog.
type BacklogSource interface {
	Backlog(ctx context.Context) (float64, error)
}

// PromQLBacklog reads the backlog from a PromQL expression.
type PromQLBacklog struct {
	collector *PrometheusCollector
	target    string
	query     string
}

// NewPromQLBacklog returns a BacklogSource for query, with $TARGET replaced by target.
func NewPromQLBacklog(c *PrometheusCollector, target, query string) *PromQLBacklog {
	return &PromQLBacklog{collector: c, target: target, query: query}
}

// Backlog runs the query; no data counts as an empty backlog.
func (b *PromQLBacklog) Backlog(ctx context.Context) (float64, error) {
	return b.collector.Scalar(ctx, b.target, b.query)
}
