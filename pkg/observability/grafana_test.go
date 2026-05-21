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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- helpers ---------------------------------------------------------------

func scaleUpRecord() DecisionRecord {
	return DecisionRecord{
		Timestamp:   time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		Deployment:  "payment-service",
		Namespace:   "production",
		OldReplicas: 3,
		NewReplicas: 6,
		Action:      "scale-up",
		Reason:      "connection pool exhausted with latency spike",
		Confidence:  0.9,
		Provider:    "rule-based",
		DryRun:      false,
	}
}

// captureServer returns an httptest.Server that records the last request body
// and responds with the given status code.
type captureServer struct {
	*httptest.Server
	lastBody   []byte
	lastHeader http.Header
	statusCode int
}

func newCaptureServer(statusCode int) *captureServer {
	cs := &captureServer{statusCode: statusCode}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.lastBody, _ = io.ReadAll(r.Body)
		cs.lastHeader = r.Header.Clone()
		w.WriteHeader(statusCode)
	}))
	return cs
}

func (cs *captureServer) parsedBody(t *testing.T) grafanaAnnotationRequest {
	t.Helper()
	var req grafanaAnnotationRequest
	if err := json.Unmarshal(cs.lastBody, &req); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}
	return req
}

func grafanaClient(srv *captureServer) *GrafanaClient {
	return newGrafanaClientWithHTTP(
		GrafanaConfig{URL: srv.URL, APIKey: "test-token"},
		srv.Client(),
	)
}

// ---- tests -----------------------------------------------------------------

func TestPushAnnotation_POSTsToAnnotationsEndpoint(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	gc := newGrafanaClientWithHTTP(GrafanaConfig{URL: srv.URL}, srv.Client())
	if err := gc.PushAnnotation(context.Background(), scaleUpRecord()); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}
	if path != "/api/annotations" {
		t.Errorf("path: want /api/annotations, got %q", path)
	}
}

func TestPushAnnotation_AnnotationTextFormat(t *testing.T) {
	cs := newCaptureServer(http.StatusOK)
	defer cs.Close()

	if err := grafanaClient(cs).PushAnnotation(context.Background(), scaleUpRecord()); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}

	got := cs.parsedBody(t)
	want := "[scale-up] payment-service: 3 → 6 replicas. connection pool exhausted with latency spike"
	if got.Text != want {
		t.Errorf("Text:\n  want: %q\n  got:  %q", want, got.Text)
	}
}

func TestPushAnnotation_Tags(t *testing.T) {
	cs := newCaptureServer(http.StatusOK)
	defer cs.Close()

	if err := grafanaClient(cs).PushAnnotation(context.Background(), scaleUpRecord()); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}

	got := cs.parsedBody(t)
	wantTags := []string{"agentic-autoscaler", "payment-service", "scale-up"}
	if len(got.Tags) != len(wantTags) {
		t.Fatalf("Tags: want %v, got %v", wantTags, got.Tags)
	}
	for i, tag := range wantTags {
		if got.Tags[i] != tag {
			t.Errorf("Tags[%d]: want %q, got %q", i, tag, got.Tags[i])
		}
	}
}

func TestPushAnnotation_TimestampIsUnixMilli(t *testing.T) {
	cs := newCaptureServer(http.StatusOK)
	defer cs.Close()

	rec := scaleUpRecord()
	if err := grafanaClient(cs).PushAnnotation(context.Background(), rec); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}

	got := cs.parsedBody(t)
	want := rec.Timestamp.UnixMilli()
	if got.Time != want {
		t.Errorf("Time: want %d, got %d", want, got.Time)
	}
}

func TestPushAnnotation_BearerTokenInAuthHeader(t *testing.T) {
	cs := newCaptureServer(http.StatusOK)
	defer cs.Close()

	if err := grafanaClient(cs).PushAnnotation(context.Background(), scaleUpRecord()); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}

	auth := cs.lastHeader.Get("Authorization")
	if auth != "Bearer test-token" {
		t.Errorf("Authorization: want %q, got %q", "Bearer test-token", auth)
	}
}

func TestPushAnnotation_ContentTypeIsJSON(t *testing.T) {
	cs := newCaptureServer(http.StatusOK)
	defer cs.Close()

	if err := grafanaClient(cs).PushAnnotation(context.Background(), scaleUpRecord()); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}

	ct := cs.lastHeader.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
}

func TestPushAnnotation_DashboardUID_IncludedWhenSet(t *testing.T) {
	cs := newCaptureServer(http.StatusOK)
	defer cs.Close()

	gc := newGrafanaClientWithHTTP(
		GrafanaConfig{URL: cs.URL, DashboardUID: "abc123"},
		cs.Client(),
	)
	if err := gc.PushAnnotation(context.Background(), scaleUpRecord()); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}

	got := cs.parsedBody(t)
	if got.DashboardUID != "abc123" {
		t.Errorf("DashboardUID: want abc123, got %q", got.DashboardUID)
	}
}

func TestPushAnnotation_DashboardUID_OmittedWhenEmpty(t *testing.T) {
	cs := newCaptureServer(http.StatusOK)
	defer cs.Close()

	gc := newGrafanaClientWithHTTP(
		GrafanaConfig{URL: cs.URL, DashboardUID: ""},
		cs.Client(),
	)
	if err := gc.PushAnnotation(context.Background(), scaleUpRecord()); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}

	// Raw JSON must not contain the dashboardUID key when empty.
	if strings.Contains(string(cs.lastBody), "dashboardUID") {
		t.Errorf("body should omit dashboardUID when empty, got: %s", cs.lastBody)
	}
}

func TestPushAnnotation_GrafanaUnreachable_ReturnsNil(t *testing.T) {
	// Point at a server that is immediately closed — connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before any request

	gc := newGrafanaClientWithHTTP(GrafanaConfig{URL: srv.URL}, srv.Client())
	err := gc.PushAnnotation(context.Background(), scaleUpRecord())
	if err != nil {
		t.Errorf("unreachable Grafana: want nil, got %v", err)
	}
}

func TestPushAnnotation_Non2xxResponse_ReturnsNil(t *testing.T) {
	for _, code := range []int{400, 401, 403, 500, 503} {
		code := code
		t.Run(http.StatusText(code), func(t *testing.T) {
			cs := newCaptureServer(code)
			defer cs.Close()

			err := grafanaClient(cs).PushAnnotation(context.Background(), scaleUpRecord())
			if err != nil {
				t.Errorf("status %d: want nil, got %v", code, err)
			}
		})
	}
}

func TestPushAnnotation_Timeout_ReturnsNil(t *testing.T) {
	// Server sleeps 200ms — longer than the 50ms client timeout so the client
	// times out, but short enough that the handler exits before srv.Close() blocks.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer srv.Close()

	// 50ms timeout fires before the server responds.
	gc := newGrafanaClientWithHTTP(
		GrafanaConfig{URL: srv.URL},
		&http.Client{Timeout: 50 * time.Millisecond},
	)
	err := gc.PushAnnotation(context.Background(), scaleUpRecord())
	if err != nil {
		t.Errorf("timeout: want nil, got %v", err)
	}
}

func TestPushAnnotation_NoAPIKey_OmitsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	gc := newGrafanaClientWithHTTP(GrafanaConfig{URL: srv.URL, APIKey: ""}, srv.Client())
	if err := gc.PushAnnotation(context.Background(), scaleUpRecord()); err != nil {
		t.Fatalf("PushAnnotation: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization: want empty when no API key, got %q", gotAuth)
	}
}

func TestAnnotationText(t *testing.T) {
	cases := []struct {
		rec  DecisionRecord
		want string
	}{
		{
			rec: DecisionRecord{
				Action: "scale-up", Deployment: "payment-service",
				OldReplicas: 3, NewReplicas: 6,
				Reason: "connection pool exhausted with latency spike",
			},
			want: "[scale-up] payment-service: 3 → 6 replicas. connection pool exhausted with latency spike",
		},
		{
			rec: DecisionRecord{
				Action: "scale-down", Deployment: "checkout",
				OldReplicas: 10, NewReplicas: 4,
				Reason: "sustained low traffic",
			},
			want: "[scale-down] checkout: 10 → 4 replicas. sustained low traffic",
		},
		{
			rec: DecisionRecord{
				Action: "hold", Deployment: "api",
				OldReplicas: 5, NewReplicas: 5,
				Reason: "steady state",
			},
			want: "[hold] api: 5 → 5 replicas. steady state",
		},
	}
	for _, tc := range cases {
		got := annotationText(tc.rec)
		if got != tc.want {
			t.Errorf("annotationText(%q):\n  want: %q\n  got:  %q", tc.rec.Action, tc.want, got)
		}
	}
}
