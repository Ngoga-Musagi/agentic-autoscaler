package reasoning

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- helpers ---------------------------------------------------------------

// openaiTestConfig returns a Config pointing at the provided mock server URL.
func openaiTestConfig(serverURL string) Config {
	return Config{
		Provider:         "openai",
		APIKey:           "test-api-key-not-a-secret",
		Model:            "gpt-test",
		BaseURL:          serverURL,
		TargetDeployment: "payment-service",
		CurrentReplicas:  5,
		MinReplicas:      2,
		MaxReplicas:      20,
	}
}

// validOpenAIEnvelope wraps an aiDecision JSON string in the Chat Completions
// response envelope the production parser expects.
func validOpenAIEnvelope(decisionJSON string) openaiResponse {
	return openaiResponse{
		ID:    "chatcmpl_test",
		Model: "gpt-test",
		Choices: []openaiChoice{
			{Message: openaiMessage{Role: "assistant", Content: decisionJSON}, FinishReason: "stop"},
		},
	}
}

func writeOpenAIJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// openaiServer returns a mock that always answers with the given decision JSON.
func openaiServer(decisionJSON string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOpenAIJSON(w, validOpenAIEnvelope(decisionJSON))
	}))
}

// ---- successful response ---------------------------------------------------

func TestOpenAIAgent_SuccessfulResponse_ParsedCorrectly(t *testing.T) {
	decisionJSON := `{"action":"scale-up","targetReplicas":8,"confidence":0.87,` +
		`"reason":"Connection pool exhausted with high latency"}`

	var capturedAuth, capturedPath string
	var capturedBody openaiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedPath = r.URL.Path
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)
		writeOpenAIJSON(w, validOpenAIEnvelope(decisionJSON))
	}))
	defer srv.Close()

	got, err := NewOpenAIAgent(openaiTestConfig(srv.URL)).Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}

	if got.Action != "scale-up" || got.TargetReplicas != 8 || got.Confidence != 0.87 {
		t.Errorf("decision: want scale-up/8/0.87, got %s/%d/%.2f", got.Action, got.TargetReplicas, got.Confidence)
	}
	if got.Provider != "openai" {
		t.Errorf("Provider: want %q, got %q", "openai", got.Provider)
	}
	if got.Timestamp == nil {
		t.Error("Timestamp: want non-nil, got nil")
	}

	if capturedAuth != "Bearer test-api-key-not-a-secret" {
		t.Errorf("Authorization header: want bearer test key, got %q", capturedAuth)
	}
	if capturedPath != "/v1/chat/completions" {
		t.Errorf("path: want /v1/chat/completions, got %q", capturedPath)
	}
	if capturedBody.Model != "gpt-test" {
		t.Errorf("model: want gpt-test, got %q", capturedBody.Model)
	}
	if capturedBody.ResponseFormat == nil || capturedBody.ResponseFormat.Type != "json_object" {
		t.Errorf("response_format: want json_object, got %+v", capturedBody.ResponseFormat)
	}
	if len(capturedBody.Messages) != 2 || capturedBody.Messages[0].Role != "system" ||
		!strings.Contains(capturedBody.Messages[1].Content, "payment-service") {
		t.Errorf("messages: want system + user prompt naming the deployment, got %+v", capturedBody.Messages)
	}
}

func TestOpenAIAgent_FencedJSON_Parsed(t *testing.T) {
	srv := openaiServer("```json\n" +
		`{"action":"hold","targetReplicas":0,"confidence":0.6,"reason":"steady"}` + "\n```")
	defer srv.Close()

	got, err := NewOpenAIAgent(openaiTestConfig(srv.URL)).Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Provider != "openai" || got.Action != "hold" {
		t.Errorf("want openai hold, got %s %s", got.Provider, got.Action)
	}
}

// ---- retries ---------------------------------------------------------------

func TestOpenAIAgent_RetryOn429_ThirdAttemptSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeOpenAIJSON(w, validOpenAIEnvelope(`{"action":"hold","targetReplicas":0,"confidence":0.5,"reason":"steady"}`))
	}))
	defer srv.Close()

	got, err := NewOpenAIAgent(openaiTestConfig(srv.URL)).Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Provider != "openai" {
		t.Errorf("Provider: want openai after successful retry, got %q", got.Provider)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("expected 3 total requests, got %d", n)
	}
}

func TestOpenAIAgent_AllRetriesExhausted_FallsBackToRuleBased(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := NewOpenAIAgent(openaiTestConfig(srv.URL)).Decide(ctx, neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not return an error (fallback), got: %v", err)
	}
	if got.Action != "hold" || got.Provider == "openai" {
		t.Errorf("want rule-based hold, got %s from %q", got.Action, got.Provider)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("expected 3 total requests, got %d", n)
	}
}

func TestOpenAIAgent_Non429ClientError_DoesNotRetry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	got, err := NewOpenAIAgent(openaiTestConfig(srv.URL)).Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback), got %q", "hold", got.Action)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("401 should not be retried: expected 1 request, got %d", n)
	}
}

// ---- invalid AI responses --------------------------------------------------

func TestOpenAIAgent_InvalidResponses_FallBack(t *testing.T) {
	cases := map[string]openaiResponse{
		"not json":          validOpenAIEnvelope("this is not json at all"),
		"out of bounds":     validOpenAIEnvelope(`{"action":"scale-up","targetReplicas":99,"confidence":0.9,"reason":"x"}`),
		"unknown action":    validOpenAIEnvelope(`{"action":"do-nothing","targetReplicas":5,"confidence":0.5,"reason":"x"}`),
		"bad confidence":    validOpenAIEnvelope(`{"action":"hold","targetReplicas":0,"confidence":1.7,"reason":"x"}`),
		"no choices":        {ID: "chatcmpl_test"},
		"empty content":     validOpenAIEnvelope(""),
		"refusal":           {Choices: []openaiChoice{{Message: openaiMessage{Refusal: "cannot help"}}}},
		"truncated content": validOpenAIEnvelope(`{"action":"scale-up","targetRep`),
	}
	for name, envelope := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeOpenAIJSON(w, envelope)
			}))
			defer srv.Close()

			got, err := NewOpenAIAgent(openaiTestConfig(srv.URL)).Decide(context.Background(), neutralSignal())
			if err != nil {
				t.Fatalf("Decide should not error (fallback), got: %v", err)
			}
			if got.Provider == "openai" {
				t.Errorf("invalid response must fall back to rules, got openai decision %+v", got)
			}
		})
	}
}
