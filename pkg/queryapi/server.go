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

// Package queryapi serves a browser UI and a JSON API that let SREs ask
// natural-language questions about past scaling decisions.
//
// Architecture:
//
//	GET  /                → embedded HTML single-page app
//	GET  /api/decisions   → last 100 DecisionRecords as JSON
//	POST /api/query       → {question, deployment?} → AI-generated plain-English answer
//
// The query handler reads the audit ConfigMap written by
// pkg/observability.ConfigMapRecorder, builds a context prompt from the
// relevant records, and calls the configured AI provider. When the AI call
// fails it falls back to a deterministic summary so the UI is always usable.
package queryapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/observability"
)

const (
	queryServerAddr    = ":8090"
	aiHTTPTimeout      = 30 * time.Second
	maxDecisionsInCtx  = 50  // decisions passed to the AI as context
	maxDecisionsInList = 100 // decisions returned by /api/decisions
)

// Server serves the interactive decision-query UI and its backing API.
type Server struct {
	k8sClient client.Client
	namespace string
	cfg       ConsoleConfig
	httpSrv   *http.Server
}

// NewServer creates a Server that reads the audit log from namespace and serves
// the management console according to cfg. Call Start to begin serving.
func NewServer(c client.Client, namespace string, cfg ConsoleConfig) *Server {
	s := &Server{k8sClient: c, namespace: namespace, cfg: cfg}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleUI)
	// Decision explorer (read-only).
	mux.HandleFunc("/api/decisions", s.handleDecisions)
	mux.HandleFunc("/api/query", s.handleQuery)
	// Console: discovery + management of AgenticAutoscaler CRs.
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/namespaces", s.handleNamespaces)
	mux.HandleFunc("/api/deployments", s.handleDeployments)
	mux.HandleFunc("/api/autoscalers", s.handleAutoscalers)
	mux.HandleFunc("/api/autoscalers/", s.handleAutoscalerItem)
	// Load generator control proxy.
	mux.HandleFunc("/api/load/start", s.handleLoadStart)
	mux.HandleFunc("/api/load/stop", s.handleLoadStop)
	mux.HandleFunc("/api/load/status", s.handleLoadStatus)

	s.httpSrv = &http.Server{
		Addr:         queryServerAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 45 * time.Second, // generous for AI round-trips
	}
	return s
}

// Start runs the HTTP server in a background goroutine and shuts it down when
// ctx is cancelled. It never blocks the caller.
func (s *Server) Start(ctx context.Context) {
	log := ctrllog.FromContext(ctx).WithName("queryapi")
	go func() {
		log.Info("query UI listening", "addr", s.httpSrv.Addr)
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "query server stopped unexpectedly")
		}
	}()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutCtx)
	}()
}

// ---- HTTP handlers ----------------------------------------------------------

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(uiHTML)
}

func (s *Server) handleDecisions(w http.ResponseWriter, r *http.Request) {
	entries, err := observability.ReadAuditLog(r.Context(), s.k8sClient, s.namespace)
	if err != nil {
		jsonError(w, "could not read audit log: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Return the most recent records first.
	entries = tail(entries, maxDecisionsInList)
	reverse(entries)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

// QueryRequest is the JSON body for POST /api/query.
type QueryRequest struct {
	Question   string `json:"question"`
	Deployment string `json:"deployment,omitempty"`
}

// QueryResponse is the JSON body returned by POST /api/query.
type QueryResponse struct {
	Answer            string `json:"answer"`
	DecisionsAnalyzed int    `json:"decisionsAnalyzed"`
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		jsonError(w, "question is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	entries, err := observability.ReadAuditLog(ctx, s.k8sClient, s.namespace)
	if err != nil {
		jsonError(w, "could not read audit log", http.StatusInternalServerError)
		return
	}

	// Narrow to the requested deployment when specified.
	if req.Deployment != "" {
		entries = filterByDeployment(entries, req.Deployment)
	}
	ctx50 := tail(entries, maxDecisionsInCtx)

	answer, err := askAI(ctx, req.Question, ctx50)
	if err != nil {
		// Graceful degradation: return a structured summary without the AI.
		answer = deterministicSummary(req.Question, ctx50)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(QueryResponse{
		Answer:            answer,
		DecisionsAnalyzed: len(ctx50),
	})
}

// ---- AI call ----------------------------------------------------------------

// anthropicRequest mirrors the Anthropic Messages API request body (subset).
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// askAI sends the question and decision records to the Anthropic Messages API
// and returns the plain-English answer. It reads ANTHROPIC_API_KEY from the
// environment — the same variable the reasoning engine uses.
func askAI(ctx context.Context, question string, records []observability.DecisionRecord) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY not set")
	}
	model := os.Getenv("ANTHROPIC_MODEL")
	if model == "" {
		model = "claude-sonnet-5"
	}

	contextJSON, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal records: %w", err)
	}

	userContent := fmt.Sprintf(
		"Here are the recent scaling decisions (JSON):\n\n%s\n\nQuestion: %s",
		string(contextJSON), question,
	)

	body := anthropicRequest{
		Model:     model,
		MaxTokens: 512,
		System: `You are an observability assistant for a Kubernetes autoscaling operator.
You help SREs understand why their applications were scaled up or down.
When answering:
- Be specific: reference exact timestamps, replica counts, and pattern names from the data.
- Explain causes in plain English: "payment-service was scaled because..."
- Keep answers under 200 words.
- If the data does not contain relevant decisions, say so clearly.`,
		Messages: []anthropicMessage{
			{Role: "user", Content: userContent},
		},
	}

	payload, _ := json.Marshal(body)

	httpClient := &http.Client{Timeout: aiHTTPTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic returned %d: %s", resp.StatusCode, string(respBody))
	}

	var ar anthropicResponse
	if err := json.Unmarshal(respBody, &ar); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	if len(ar.Content) == 0 {
		return "", fmt.Errorf("empty response from anthropic")
	}
	return ar.Content[0].Text, nil
}

// deterministicSummary produces a structured plain-English answer without an
// AI call. It is the fallback when the API key is absent or the call fails.
func deterministicSummary(question string, records []observability.DecisionRecord) string {
	if len(records) == 0 {
		return "No decisions found in the audit log for the requested scope."
	}

	var scaleUps, scaleDowns, holds int
	var reasons []string
	for _, r := range records {
		switch r.Action {
		case "scale-up":
			scaleUps++
			if r.Reason != "" && len(reasons) < 3 {
				reasons = append(reasons, fmt.Sprintf("• %s: %s", r.Timestamp.Format("Jan 2 15:04"), r.Reason))
			}
		case "scale-down":
			scaleDowns++
		default:
			holds++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Over the %d decisions in context: %d scale-ups, %d scale-downs, %d holds.\n",
		len(records), scaleUps, scaleDowns, holds)
	if len(reasons) > 0 {
		b.WriteString("\nRecent scale-up reasons:\n")
		b.WriteString(strings.Join(reasons, "\n"))
	}
	b.WriteString("\n\n(AI provider unavailable — connect ANTHROPIC_API_KEY for natural-language answers.)")
	return b.String()
}

// ---- helpers ----------------------------------------------------------------

func filterByDeployment(entries []observability.DecisionRecord, name string) []observability.DecisionRecord {
	out := entries[:0]
	for _, e := range entries {
		if strings.EqualFold(e.Deployment, name) || strings.EqualFold(e.Namespace+"/"+e.Deployment, name) {
			out = append(out, e)
		}
	}
	return out
}

func tail[T any](s []T, n int) []T {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
