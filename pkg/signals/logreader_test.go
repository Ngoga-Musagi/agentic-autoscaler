package signals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- helpers ---------------------------------------------------------------

// lokiFixture builds a single-stream Loki query_range response body.
func lokiFixture(streamLabels map[string]string, values [][]string) lokiResponse {
	return lokiResponse{
		Status: "success",
		Data: lokiData{
			ResultType: "streams",
			Result: []lokiStream{
				{Stream: streamLabels, Values: values},
			},
		},
	}
}

func writeLokiJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// nsStr converts a time.Time to a Loki nanosecond timestamp string.
func nsStr(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

// ---- tests -----------------------------------------------------------------

func TestLokiReader_Read_ParsesEntries(t *testing.T) {
	ts1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 1, 1, 12, 0, 1, 500000000, time.UTC)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeLokiJSON(w, lokiFixture(
			map[string]string{"app": "payment-service", "namespace": "production"},
			[][]string{
				{nsStr(ts1), "request completed in 45ms"},
				{nsStr(ts2), "connection pool exhausted after 30s"},
			},
		))
	}))
	defer srv.Close()

	r := NewLokiReader(srv.URL, `{app="payment-service"}`)
	entries, err := r.Read(context.Background(), 5)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	e0 := entries[0]
	if e0.Timestamp.UnixNano() != ts1.UnixNano() {
		t.Errorf("entry[0].Timestamp: want %v, got %v", ts1, e0.Timestamp)
	}
	if e0.Message != "request completed in 45ms" {
		t.Errorf("entry[0].Message: want %q, got %q", "request completed in 45ms", e0.Message)
	}
	if e0.Labels["app"] != "payment-service" {
		t.Errorf("entry[0].Labels[app]: want %q, got %q", "payment-service", e0.Labels["app"])
	}
	if e0.Labels["namespace"] != "production" {
		t.Errorf("entry[0].Labels[namespace]: want %q, got %q", "production", e0.Labels["namespace"])
	}
	if entries[1].Message != "connection pool exhausted after 30s" {
		t.Errorf("entry[1].Message: got %q", entries[1].Message)
	}
}

func TestLokiReader_Read_SendsCorrectQueryParams(t *testing.T) {
	var capturedQuery, capturedStart, capturedEnd, capturedLimit string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query().Get("query")
		capturedStart = r.URL.Query().Get("start")
		capturedEnd = r.URL.Query().Get("end")
		capturedLimit = r.URL.Query().Get("limit")
		writeLokiJSON(w, lokiResponse{Status: "success", Data: lokiData{Result: nil}})
	}))
	defer srv.Close()

	logql := `{namespace="production",app="payment-service"}`
	before := time.Now().UTC()
	r := NewLokiReader(srv.URL, logql)
	if _, err := r.Read(context.Background(), 5); err != nil {
		t.Fatalf("Read: %v", err)
	}
	after := time.Now().UTC()

	if capturedQuery != logql {
		t.Errorf("query param: want %q, got %q", logql, capturedQuery)
	}
	if capturedLimit != "500" {
		t.Errorf("limit param: want %q, got %q", "500", capturedLimit)
	}

	// start must be approximately (now - 5 minutes).
	startNs, err := strconv.ParseInt(capturedStart, 10, 64)
	if err != nil {
		t.Fatalf("parse start param %q: %v", capturedStart, err)
	}
	startTime := time.Unix(0, startNs).UTC()
	wantStart := before.Add(-5 * time.Minute)
	if startTime.Before(wantStart.Add(-2*time.Second)) || startTime.After(after) {
		t.Errorf("start param %v outside expected window [%v, %v]", startTime, wantStart, after)
	}

	// end must be close to now.
	endNs, err := strconv.ParseInt(capturedEnd, 10, 64)
	if err != nil {
		t.Fatalf("parse end param %q: %v", capturedEnd, err)
	}
	endTime := time.Unix(0, endNs).UTC()
	if endTime.Before(before) || endTime.After(after.Add(time.Second)) {
		t.Errorf("end param %v outside expected window [%v, %v]", endTime, before, after)
	}
}

func TestLokiReader_Read_StripsANSICodes(t *testing.T) {
	ts := time.Now().UTC()
	raw := "\x1b[32mINFO\x1b[0m request completed \x1b[1;31mERROR\x1b[0m in 45ms"
	want := "INFO request completed ERROR in 45ms"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLokiJSON(w, lokiFixture(
			map[string]string{"app": "svc"},
			[][]string{{nsStr(ts), raw}},
		))
	}))
	defer srv.Close()

	r := NewLokiReader(srv.URL, `{app="svc"}`)
	entries, err := r.Read(context.Background(), 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Message != want {
		t.Errorf("ANSI strip: want %q, got %q", want, entries[0].Message)
	}
}

func TestLokiReader_Read_TruncatesLongMessages(t *testing.T) {
	ts := time.Now().UTC()
	long := strings.Repeat("x", maxLogMessageBytes+500)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLokiJSON(w, lokiFixture(
			map[string]string{"app": "svc"},
			[][]string{{nsStr(ts), long}},
		))
	}))
	defer srv.Close()

	r := NewLokiReader(srv.URL, `{app="svc"}`)
	entries, err := r.Read(context.Background(), 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries[0].Message) != maxLogMessageBytes {
		t.Errorf("truncation: want len %d, got %d", maxLogMessageBytes, len(entries[0].Message))
	}
}

func TestLokiReader_Read_MultipleStreams(t *testing.T) {
	ts := time.Now().UTC()
	tsStr := nsStr(ts)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLokiJSON(w, lokiResponse{
			Status: "success",
			Data: lokiData{
				ResultType: "streams",
				Result: []lokiStream{
					{
						Stream: map[string]string{"pod": "payment-service-abc"},
						Values: [][]string{{tsStr, "line from pod abc"}},
					},
					{
						Stream: map[string]string{"pod": "payment-service-def"},
						Values: [][]string{{tsStr, "line from pod def"}},
					},
				},
			},
		})
	}))
	defer srv.Close()

	r := NewLokiReader(srv.URL, `{app="payment-service"}`)
	entries, err := r.Read(context.Background(), 5)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries across 2 streams, got %d", len(entries))
	}
}

func TestLokiReader_Read_EmptyResult_NoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLokiJSON(w, lokiResponse{
			Status: "success",
			Data:   lokiData{ResultType: "streams", Result: nil},
		})
	}))
	defer srv.Close()

	r := NewLokiReader(srv.URL, `{app="quiet-service"}`)
	entries, err := r.Read(context.Background(), 5)
	if err != nil {
		t.Fatalf("Read on empty result: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestLokiReader_Read_TrailingSlashInURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		writeLokiJSON(w, lokiResponse{Status: "success", Data: lokiData{Result: nil}})
	}))
	defer srv.Close()

	r := NewLokiReader(srv.URL+"/", `{app="svc"}`)
	if _, err := r.Read(context.Background(), 1); err != nil {
		t.Fatalf("Read with trailing slash: %v", err)
	}
}

func TestLokiReader_Read_HTTPError_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer srv.Close()

	r := NewLokiReader(srv.URL, `{app="svc"}`)
	_, err := r.Read(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error on HTTP 502, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error should mention 502, got: %v", err)
	}
}

func TestLokiReader_Read_LokiErrorStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLokiJSON(w, lokiResponse{
			Status: "error",
			Error:  "parse error at line 1",
		})
	}))
	defer srv.Close()

	r := NewLokiReader(srv.URL, `{bad query`)
	_, err := r.Read(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error for loki status=error, got nil")
	}
	if !strings.Contains(err.Error(), "parse error") {
		t.Errorf("error should contain loki message, got: %v", err)
	}
}

func TestLokiReader_Read_ContextCancelled_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLokiJSON(w, lokiResponse{Status: "success", Data: lokiData{Result: nil}})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := NewLokiReader(srv.URL, `{app="svc"}`)
	_, err := r.Read(ctx, 5)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}
