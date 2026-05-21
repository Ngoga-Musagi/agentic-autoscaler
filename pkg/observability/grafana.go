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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const grafanaTimeout = 5 * time.Second

// GrafanaConfig holds the connection parameters for the Grafana annotation API.
// All fields are sourced from environment variables so the operator binary never
// embeds credentials at compile time.
type GrafanaConfig struct {
	// URL is the base URL of the Grafana instance, e.g. "http://grafana.monitoring:3000".
	URL string

	// APIKey is the Bearer token for the Grafana HTTP API, sourced from a K8s Secret.
	APIKey string

	// DashboardUID scopes annotations to one dashboard. When empty the annotation
	// is global and appears on every Grafana dashboard.
	DashboardUID string
}

// GrafanaConfigFromEnv builds a GrafanaConfig by reading environment variables.
// The caller is responsible for checking whether URL is non-empty before deciding
// whether to construct a GrafanaClient.
func GrafanaConfigFromEnv() GrafanaConfig {
	return GrafanaConfig{
		URL:          os.Getenv("GRAFANA_URL"),
		APIKey:       os.Getenv("GRAFANA_API_KEY"),
		DashboardUID: os.Getenv("GRAFANA_DASHBOARD_UID"),
	}
}

// grafanaAnnotationRequest is the JSON body sent to POST /api/annotations.
type grafanaAnnotationRequest struct {
	Time         int64    `json:"time"`
	Tags         []string `json:"tags"`
	Text         string   `json:"text"`
	DashboardUID string   `json:"dashboardUID,omitempty"`
}

// GrafanaClient pushes scale-decision annotations to a Grafana instance so
// on-call SREs see a labelled vertical line on every dashboard at the exact
// moment a scale event fired.
type GrafanaClient struct {
	cfg    GrafanaConfig
	client *http.Client
}

// NewGrafanaClient returns a GrafanaClient configured from cfg.
// A nil http.Client is replaced with a client that enforces grafanaTimeout.
func NewGrafanaClient(cfg GrafanaConfig) *GrafanaClient {
	return &GrafanaClient{
		cfg: cfg,
		client: &http.Client{
			Timeout: grafanaTimeout,
		},
	}
}

// newGrafanaClientWithHTTP is used by tests to inject a custom *http.Client
// (e.g. one pointed at an httptest.Server).
func newGrafanaClientWithHTTP(cfg GrafanaConfig, hc *http.Client) *GrafanaClient {
	return &GrafanaClient{cfg: cfg, client: hc}
}

// PushAnnotation posts a visual marker to Grafana for the given DecisionRecord.
// Grafana unreachability is treated as a warning, never an error — a broken
// Grafana instance must never block reconcile or scale actions.
func (g *GrafanaClient) PushAnnotation(ctx context.Context, r DecisionRecord) error {
	logger := ctrllog.FromContext(ctx).WithName("grafana")

	body := grafanaAnnotationRequest{
		Time: r.Timestamp.UnixMilli(),
		Tags: []string{"agentic-autoscaler", r.Deployment, r.Action},
		Text: annotationText(r),
	}
	if g.cfg.DashboardUID != "" {
		body.DashboardUID = g.cfg.DashboardUID
	}

	payload, err := json.Marshal(body)
	if err != nil {
		// json.Marshal of a fixed struct cannot fail in practice.
		return fmt.Errorf("marshal grafana annotation: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.cfg.URL+"/api/annotations", bytes.NewReader(payload))
	if err != nil {
		logger.Error(err, "grafana: could not build request (check GRAFANA_URL)")
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if g.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		// Network errors (unreachable, timeout) are warnings, not failures.
		logger.Info("grafana: annotation skipped — Grafana unreachable",
			"error", err.Error(),
			"deployment", r.Deployment)
		return nil
	}
	defer resp.Body.Close()
	// Drain to allow connection reuse.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Info("grafana: annotation returned non-2xx",
			"status", resp.StatusCode,
			"deployment", r.Deployment)
		// Non-fatal: Grafana returning 4xx/5xx is a configuration problem, not
		// a reason to fail the reconcile loop.
		return nil
	}

	logger.V(1).Info("grafana: annotation pushed",
		"deployment", r.Deployment,
		"action", r.Action)
	return nil
}

// annotationText formats the human-readable annotation body shown in Grafana
// tooltips.  Example:
//
//	[scale-up] payment-service: 3 → 6 replicas. connection pool exhausted with latency spike
func annotationText(r DecisionRecord) string {
	return fmt.Sprintf("[%s] %s: %d → %d replicas. %s",
		r.Action, r.Deployment, r.OldReplicas, r.NewReplicas, r.Reason)
}
