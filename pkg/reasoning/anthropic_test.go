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

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/signals"
)

// ---- helpers ---------------------------------------------------------------

// anthropicTestConfig returns a Config pointing at the provided mock server URL.
func anthropicTestConfig(serverURL string) Config {
	return Config{
		Provider:         "anthropic",
		APIKey:           "test-api-key-not-a-secret",
		Model:            "claude-test",
		BaseURL:          serverURL,
		TargetDeployment: "payment-service",
		CurrentReplicas:  5,
		MinReplicas:      2,
		MaxReplicas:      20,
	}
}

// validAnthropicEnvelope wraps an aiDecision JSON string in the Anthropic
// response envelope the production parser expects.
func validAnthropicEnvelope(decisionJSON string) anthropicResponse {
	return anthropicResponse{
		ID:    "msg_test",
		Type:  "message",
		Model: "claude-test",
		Content: []anthropicContent{
			{Type: "text", Text: decisionJSON},
		},
	}
}

func writeAnthropicJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// neutralSignal returns a FusedSignal that causes the rule-based fallback to "hold".
func neutralSignal() fusion.FusedSignal {
	return fusion.FusedSignal{
		Snapshot: signals.SystemSnapshot{
			Metrics: signals.MetricSnapshot{
				LatencyP99Ms: 200,
				CPUUtilPct:   50,
				ErrorRatePct: 2,
			},
		},
	}
}

// ---- successful response ---------------------------------------------------

func TestAnthropicAgent_SuccessfulResponse_ParsedCorrectly(t *testing.T) {
	decisionJSON := `{"action":"scale-up","targetReplicas":8,"confidence":0.87,"reason":"Connection pool exhausted with high latency"}`

	var capturedAPIKey, capturedVersion, capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAPIKey = r.Header.Get("x-api-key")
		capturedVersion = r.Header.Get("anthropic-version")
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedBody = string(bodyBytes)
		writeAnthropicJSON(w, validAnthropicEnvelope(decisionJSON))
	}))
	defer srv.Close()

	agent := NewAnthropicAgent(anthropicTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}

	// Decision fields
	if got.Action != "scale-up" {
		t.Errorf("Action: want %q, got %q", "scale-up", got.Action)
	}
	if got.TargetReplicas != 8 {
		t.Errorf("TargetReplicas: want 8, got %d", got.TargetReplicas)
	}
	if got.Confidence != 0.87 {
		t.Errorf("Confidence: want 0.87, got %f", got.Confidence)
	}
	if !strings.Contains(got.Reason, "Connection pool") {
		t.Errorf("Reason: want substring %q, got %q", "Connection pool", got.Reason)
	}
	if got.Timestamp == nil {
		t.Error("Timestamp: want non-nil, got nil")
	}

	// Security: API key must be sent in header — but we check it arrived, not log it.
	if capturedAPIKey != "test-api-key-not-a-secret" {
		t.Errorf("x-api-key header: want test key, got %q", capturedAPIKey)
	}
	if capturedVersion != anthropicAPIVersion {
		t.Errorf("anthropic-version header: want %q, got %q", anthropicAPIVersion, capturedVersion)
	}

	// Request body must include the model name and the deployment name.
	if !strings.Contains(capturedBody, "claude-test") {
		t.Errorf("request body should contain model name, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "payment-service") {
		t.Errorf("request body should contain deployment name from BuildPrompt, got: %s", capturedBody)
	}
}

// ---- retry on 429 ----------------------------------------------------------

func TestAnthropicAgent_RetryOn429_ThirdAttemptSucceeds(t *testing.T) {
	var attempts atomic.Int32
	decisionJSON := `{"action":"hold","targetReplicas":0,"confidence":0.5,"reason":"Steady state"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeAnthropicJSON(w, validAnthropicEnvelope(decisionJSON))
	}))
	defer srv.Close()

	// Shorten backoff to keep the test fast.
	cfg := anthropicTestConfig(srv.URL)
	a := NewAnthropicAgent(cfg).(*AnthropicAgent)
	// Override backoff via a short-circuit: we will directly test callWithRetry.
	// For the end-to-end Decide test we accept the real 500ms×2 cost, or we
	// can drive it through the struct with a patched client.
	// Use a context with a generous deadline so the real backoffs don't time out.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := a.Decide(ctx, neutralSignal())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q, got %q (wrong attempt succeeded)", "hold", got.Action)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("expected 3 total requests (1 initial + 2 retries), got %d", n)
	}
}

func TestAnthropicAgent_RetryOn500_ThirdAttemptSucceeds(t *testing.T) {
	var attempts atomic.Int32
	decisionJSON := `{"action":"scale-down","targetReplicas":3,"confidence":0.6,"reason":"All clear"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeAnthropicJSON(w, validAnthropicEnvelope(decisionJSON))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agent := NewAnthropicAgent(anthropicTestConfig(srv.URL))
	got, err := agent.Decide(ctx, neutralSignal())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Action != "scale-down" {
		t.Errorf("Action: want %q, got %q", "scale-down", got.Action)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("expected 3 total requests, got %d", n)
	}
}

func TestAnthropicAgent_AllRetriesExhausted_FallsBackToRuleBased(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agent := NewAnthropicAgent(anthropicTestConfig(srv.URL))
	got, err := agent.Decide(ctx, neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not return an error (fallback), got: %v", err)
	}
	// Rule-based fallback on the neutral signal returns "hold".
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback), got %q", "hold", got.Action)
	}
	// Total attempts = 1 initial + 2 retries = 3.
	if n := attempts.Load(); n != 3 {
		t.Errorf("expected 3 total requests, got %d", n)
	}
}

// ---- timeout / fallback ----------------------------------------------------

func TestAnthropicAgent_Timeout_FallsBackToRuleBased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay longer than the test context will allow.
		time.Sleep(300 * time.Millisecond)
		writeAnthropicJSON(w, validAnthropicEnvelope(`{"action":"scale-up","targetReplicas":8,"confidence":0.9,"reason":"test"}`))
	}))
	defer srv.Close()

	// A 50ms context deadline expires well before the 300ms server delay.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	agent := NewAnthropicAgent(anthropicTestConfig(srv.URL))
	got, err := agent.Decide(ctx, neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not return an error (fallback), got: %v", err)
	}
	// Rule-based fallback on a neutral signal returns "hold".
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback after timeout), got %q", "hold", got.Action)
	}
}

// ---- invalid AI responses --------------------------------------------------

func TestAnthropicAgent_InvalidJSONFromAI_FallsBackToRuleBased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAnthropicJSON(w, validAnthropicEnvelope("this is not json at all"))
	}))
	defer srv.Close()

	agent := NewAnthropicAgent(anthropicTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error on invalid AI JSON (fallback), got: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback), got %q", "hold", got.Action)
	}
}

func TestAnthropicAgent_OutOfBoundsReplicas_FallsBack(t *testing.T) {
	// targetReplicas=99 exceeds MaxReplicas=20 — validation should reject it.
	decisionJSON := `{"action":"scale-up","targetReplicas":99,"confidence":0.9,"reason":"test"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAnthropicJSON(w, validAnthropicEnvelope(decisionJSON))
	}))
	defer srv.Close()

	agent := NewAnthropicAgent(anthropicTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.TargetReplicas == 99 {
		t.Error("out-of-bounds targetReplicas should have been rejected; fallback should have returned a safe value")
	}
}

func TestAnthropicAgent_UnknownAction_FallsBack(t *testing.T) {
	decisionJSON := `{"action":"do-nothing","targetReplicas":5,"confidence":0.5,"reason":"test"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAnthropicJSON(w, validAnthropicEnvelope(decisionJSON))
	}))
	defer srv.Close()

	agent := NewAnthropicAgent(anthropicTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.Action == "do-nothing" {
		t.Error("unknown action should have been rejected and replaced with fallback decision")
	}
}

func TestAnthropicAgent_Non429ClientError_DoesNotRetry(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		// 400 Bad Request is not retryable.
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	agent := NewAnthropicAgent(anthropicTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback), got %q", "hold", got.Action)
	}
	// 400 must not be retried — exactly one request.
	if n := attempts.Load(); n != 1 {
		t.Errorf("400 should not be retried: expected 1 request, got %d", n)
	}
}

// ---- prompt tests ----------------------------------------------------------

func TestBuildPrompt_ContainsRequiredFields(t *testing.T) {
	cfg := Config{
		TargetDeployment: "checkout-service",
		CurrentReplicas:  4,
		MinReplicas:      2,
		MaxReplicas:      10,
	}
	signal := fusion.FusedSignal{
		Snapshot: signals.SystemSnapshot{
			Metrics: signals.MetricSnapshot{
				LatencyP99Ms:   1450.5,
				ErrorRatePct:   7.3,
				CPUUtilPct:     62.0,
				RequestsPerSec: 320.0,
			},
		},
		MatchedPatterns: []fusion.PatternMatch{
			{Name: "upstream-timeout", Severity: "warning", Count: 3},
		},
		SeverityScore: 0.5,
	}

	prompt := BuildPrompt(cfg, signal)

	checks := []string{
		"checkout-service",
		"4",    // current replicas
		"2",    // min
		"10",   // max
		"1450", // latency (truncated to 1450.5ms)
		"7.30", // error rate
		"62.0", // CPU
		"upstream-timeout",
		"warning",
		"3",    // count
		"0.50", // severity score
	}
	for _, want := range checks {
		if !strings.Contains(prompt, want) {
			t.Errorf("BuildPrompt: want %q in output, not found in:\n%s", want, prompt)
		}
	}
}

func TestBuildPrompt_NoPatternsShowsNone(t *testing.T) {
	cfg := Config{TargetDeployment: "api", MinReplicas: 1, MaxReplicas: 5}
	signal := fusion.FusedSignal{} // no patterns, zero metrics

	prompt := BuildPrompt(cfg, signal)
	if !strings.Contains(prompt, "none") {
		t.Errorf("BuildPrompt with no patterns should contain %q, got:\n%s", "none", prompt)
	}
}

func TestSystemPrompt_InstructsJSONOnly(t *testing.T) {
	if !strings.Contains(SystemPrompt, "ONLY") {
		t.Error("SystemPrompt should emphasise JSON-only response with 'ONLY'")
	}
	if !strings.Contains(SystemPrompt, "action") || !strings.Contains(SystemPrompt, "targetReplicas") {
		t.Error("SystemPrompt should document the required JSON keys")
	}
}
