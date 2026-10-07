package reasoning

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

// ollamaTestConfig returns a Config pointing at the provided mock server URL.
func ollamaTestConfig(serverURL string) Config {
	return Config{
		Provider:         "ollama",
		Model:            "llama3:8b",
		BaseURL:          serverURL,
		TargetDeployment: "payment-service",
		CurrentReplicas:  5,
		MinReplicas:      2,
		MaxReplicas:      20,
	}
}

// validOllamaEnvelope wraps an aiDecision JSON string in the Ollama response
// envelope the production parser expects.
func validOllamaEnvelope(decisionJSON string) ollamaResponse {
	return ollamaResponse{
		Model:    "llama3:8b",
		Response: decisionJSON,
		Done:     true,
	}
}

func writeOllamaJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- successful response ---------------------------------------------------

func TestOllamaAgent_SuccessfulResponse_ParsedCorrectly(t *testing.T) {
	decisionJSON := `{"action":"scale-up","targetReplicas":8,"confidence":0.82,"reason":"High latency detected on payment-service"}`

	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedBody = string(bodyBytes)
		writeOllamaJSON(w, validOllamaEnvelope(decisionJSON))
	}))
	defer srv.Close()

	agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}

	if got.Action != "scale-up" {
		t.Errorf("Action: want %q, got %q", "scale-up", got.Action)
	}
	if got.TargetReplicas != 8 {
		t.Errorf("TargetReplicas: want 8, got %d", got.TargetReplicas)
	}
	if got.Confidence != 0.82 {
		t.Errorf("Confidence: want 0.82, got %f", got.Confidence)
	}
	if !strings.Contains(got.Reason, "High latency") {
		t.Errorf("Reason: want substring %q, got %q", "High latency", got.Reason)
	}
	if got.Timestamp == nil {
		t.Error("Timestamp: want non-nil, got nil")
	}

	// Request body must include the model name and stream:false.
	if !strings.Contains(capturedBody, "llama3:8b") {
		t.Errorf("request body should contain model name, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, `"stream":false`) {
		t.Errorf("request body should contain stream:false, got: %s", capturedBody)
	}
	// Prompt must include the deployment name from BuildPrompt.
	if !strings.Contains(capturedBody, "payment-service") {
		t.Errorf("request body should contain deployment name, got: %s", capturedBody)
	}
}

// ---- timeout / no retry ----------------------------------------------------

func TestOllamaAgent_Timeout_FallsBackImmediately(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		// Delay longer than the test context will allow.
		time.Sleep(300 * time.Millisecond)
		writeOllamaJSON(w, validOllamaEnvelope(`{"action":"scale-up","targetReplicas":8,"confidence":0.9,"reason":"test"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
	got, err := agent.Decide(ctx, neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not return error (fallback), got: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback after timeout), got %q", "hold", got.Action)
	}
	// Ollama must NOT retry — exactly one request.
	if attempts != 1 {
		t.Errorf("ollama should not retry on timeout: expected 1 request, got %d", attempts)
	}
}

// ---- invalid AI responses --------------------------------------------------

func TestOllamaAgent_InvalidJSONFromAI_FallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOllamaJSON(w, validOllamaEnvelope("this is not valid json"))
	}))
	defer srv.Close()

	agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback), got %q", "hold", got.Action)
	}
}

func TestOllamaAgent_EmptyResponseField_FallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOllamaJSON(w, ollamaResponse{Model: "llama3:8b", Response: "", Done: true})
	}))
	defer srv.Close()

	agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback), got %q", "hold", got.Action)
	}
}

func TestOllamaAgent_OutOfBoundsReplicas_FallsBack(t *testing.T) {
	// targetReplicas=99 exceeds MaxReplicas=20 — validation should reject it.
	decisionJSON := `{"action":"scale-up","targetReplicas":99,"confidence":0.9,"reason":"test"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOllamaJSON(w, validOllamaEnvelope(decisionJSON))
	}))
	defer srv.Close()

	agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.TargetReplicas == 99 {
		t.Error("out-of-bounds targetReplicas should have been rejected; fallback should return safe value")
	}
}

func TestOllamaAgent_UnknownAction_FallsBack(t *testing.T) {
	decisionJSON := `{"action":"reboot","targetReplicas":5,"confidence":0.5,"reason":"test"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOllamaJSON(w, validOllamaEnvelope(decisionJSON))
	}))
	defer srv.Close()

	agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.Action == "reboot" {
		t.Error("unknown action should have been rejected and replaced with fallback decision")
	}
}

func TestOllamaAgent_ServerError_FallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
	got, err := agent.Decide(context.Background(), neutralSignal())
	if err != nil {
		t.Fatalf("Decide should not error (fallback), got: %v", err)
	}
	if got.Action != "hold" {
		t.Errorf("Action: want %q (rule-based fallback), got %q", "hold", got.Action)
	}
}

// ---- model selection -------------------------------------------------------

func TestOllamaAgent_DefaultModel_UsedWhenModelEmpty(t *testing.T) {
	var capturedModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		capturedModel = req.Model
		writeOllamaJSON(w, validOllamaEnvelope(`{"action":"hold","targetReplicas":0,"confidence":0.5,"reason":"steady"}`))
	}))
	defer srv.Close()

	cfg := ollamaTestConfig(srv.URL)
	cfg.Model = "" // force default
	agent := NewOllamaAgent(cfg)
	_, _ = agent.Decide(context.Background(), neutralSignal())

	if capturedModel != ollamaDefaultModel {
		t.Errorf("default model: want %q, got %q", ollamaDefaultModel, capturedModel)
	}
}

// ---- small-model hardening -------------------------------------------------

// TestOllamaAgent_RequestConstrainsJSON asserts the request asks Ollama to
// constrain output to JSON with a deterministic (temperature 0) sampler — the
// two settings that make small models reliably emit the decision object.
func TestOllamaAgent_RequestConstrainsJSON(t *testing.T) {
	var req ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeOllamaJSON(w, validOllamaEnvelope(`{"action":"hold","targetReplicas":0,"confidence":0.5,"reason":"steady"}`))
	}))
	defer srv.Close()

	agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
	if _, err := agent.Decide(context.Background(), neutralSignal()); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if req.Format != "json" {
		t.Errorf("request Format: want %q, got %q", "json", req.Format)
	}
	if req.Options == nil || req.Options.Temperature != 0 {
		t.Errorf("request Options.Temperature: want 0, got %+v", req.Options)
	}
}

// TestOllamaAgent_WrappedJSON_Parsed proves the parser tolerates the markdown
// fences and surrounding prose that small models emit despite format:json.
func TestOllamaAgent_WrappedJSON_Parsed(t *testing.T) {
	decision := `{"action":"scale-up","targetReplicas":8,"confidence":0.8,"reason":"pool exhausted"}`
	braceReason := `{"action":"scale-up","targetReplicas":8,"confidence":0.8,"reason":"pods {app=foo} saturated"}`
	cases := map[string]string{
		"markdown fence":        "```json\n" + decision + "\n```",
		"bare fence":            "```\n" + decision + "\n```",
		"prose around json":     "Here is my decision:\n" + decision + "\nHope that helps.",
		"leading whitespace":    "   \n" + decision,
		"braces in reason":      braceReason,
		"trailing prose braces": decision + "\n(note: {done})",
	}
	for name, wrapped := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeOllamaJSON(w, validOllamaEnvelope(wrapped))
			}))
			defer srv.Close()

			agent := NewOllamaAgent(ollamaTestConfig(srv.URL))
			got, err := agent.Decide(context.Background(), neutralSignal())
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got.Action != "scale-up" || got.TargetReplicas != 8 {
				t.Errorf("want scale-up/8 from wrapped JSON, got %s/%d", got.Action, got.TargetReplicas)
			}
		})
	}
}

func TestExtractJSONObject(t *testing.T) {
	obj := `{"action":"hold"}`
	cases := []struct {
		in, want string
	}{
		{obj, obj},
		{"```json\n" + obj + "\n```", obj},
		{"```\n" + obj + "\n```", obj},
		{"prefix " + obj + " suffix", obj},
		{"  " + obj + "  ", obj},
		{"no json here", "no json here"},
		// Braces inside a string value must not end the object early.
		{`{"reason":"a}b"}`, `{"reason":"a}b"}`},
		// Trailing content with its own braces must be dropped.
		{obj + " {trailing}", obj},
		// Escaped quote inside a string is handled.
		{`{"reason":"say \"hi\" }"}`, `{"reason":"say \"hi\" }"}`},
	}
	for _, c := range cases {
		if got := extractJSONObject(c.in); got != c.want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
