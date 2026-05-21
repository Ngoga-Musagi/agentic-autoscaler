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

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	lokiHTTPTimeout    = 10 * time.Second
	lokiQueryLimit     = 500
	maxLogMessageBytes = 2000
)

// ansiEscapeRe matches ANSI CSI escape sequences (colour codes, cursor movement, etc.)
// so they can be stripped from log lines before storage.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// LokiReader fetches log lines from the Loki query_range HTTP API.
type LokiReader struct {
	baseURL string
	query   string
	client  *http.Client
}

// NewLokiReader returns a LokiReader that queries lokiURL with the provided
// LogQL logqlQuery. The 10-second HTTP client timeout is separate from any
// context deadline; the shorter of the two takes effect.
func NewLokiReader(lokiURL, logqlQuery string) *LokiReader {
	return &LokiReader{
		baseURL: strings.TrimRight(lokiURL, "/"),
		query:   logqlQuery,
		client:  &http.Client{Timeout: lokiHTTPTimeout},
	}
}

// Read fetches log entries covering the last lookbackMinutes minutes and returns
// them as []LogEntry. An empty result is not an error — it means no matching log
// lines were produced in the window.
func (r *LokiReader) Read(ctx context.Context, lookbackMinutes int) ([]LogEntry, error) {
	now := time.Now().UTC()
	start := now.Add(-time.Duration(lookbackMinutes) * time.Minute)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/loki/api/v1/query_range", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	q := req.URL.Query()
	q.Set("query", r.query)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(now.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(lokiQueryLimit))
	req.URL.RawQuery = q.Encode()

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki returned status %d: %s", resp.StatusCode, body)
	}

	var lr lokiResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if lr.Status != "success" {
		return nil, fmt.Errorf("loki error: %s", lr.Error)
	}

	return parseStreams(lr.Data.Result), nil
}

// parseStreams converts the Loki streams result into a flat slice of LogEntry.
func parseStreams(streams []lokiStream) []LogEntry {
	var entries []LogEntry
	for _, stream := range streams {
		for _, v := range stream.Values {
			if len(v) < 2 {
				continue
			}
			ts, err := parseLokiTimestamp(v[0])
			if err != nil {
				continue // skip malformed timestamps rather than aborting
			}
			entries = append(entries, LogEntry{
				Timestamp: ts,
				Message:   sanitize(v[1]),
				Labels:    stream.Stream,
			})
		}
	}
	return entries
}

// parseLokiTimestamp converts a Loki nanosecond Unix timestamp string to time.Time.
func parseLokiTimestamp(nsStr string) (time.Time, error) {
	ns, err := strconv.ParseInt(nsStr, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", nsStr, err)
	}
	return time.Unix(0, ns).UTC(), nil
}

// sanitize strips ANSI escape codes and truncates the message to maxLogMessageBytes.
func sanitize(msg string) string {
	msg = ansiEscapeRe.ReplaceAllString(msg, "")
	if len(msg) > maxLogMessageBytes {
		msg = msg[:maxLogMessageBytes]
	}
	return msg
}

// ---- Loki HTTP API response types ----------------------------------------

type lokiResponse struct {
	Status string   `json:"status"`
	Data   lokiData `json:"data"`
	Error  string   `json:"error,omitempty"`
}

type lokiData struct {
	ResultType string       `json:"resultType"`
	Result     []lokiStream `json:"result"`
}

// lokiStream represents one label-set + its log lines.
// Values elements are [nanosecond_unix_timestamp_string, log_line_string].
type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}
