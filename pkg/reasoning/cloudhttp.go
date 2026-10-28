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

package reasoning

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// cloudPostWithRetry POSTs a JSON body, retrying on 429 or 5xx up to
// maxRetries times with a fixed backoff. Both cloud providers share this one
// retry contract (ai-provider.md), so it lives here rather than per provider.
// Context cancellation stops retries immediately. headers may carry the API
// key; they are never logged or included in returned errors.
func cloudPostWithRetry(
	ctx context.Context, c *http.Client, url string, headers map[string]string,
	body []byte, maxRetries int, backoff time.Duration,
) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		respBody, status, err := cloudPost(ctx, c, url, headers, body)
		if err != nil {
			lastErr = fmt.Errorf("attempt %d: %w", attempt+1, err)
			continue // network/context error — retry
		}
		if status == http.StatusOK {
			return respBody, nil
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = fmt.Errorf("attempt %d: http %d", attempt+1, status)
			continue // retryable server error
		}
		// Non-retryable client error (4xx other than 429).
		return nil, fmt.Errorf("non-retryable http %d: %s", status, respBody)
	}
	return nil, fmt.Errorf("all %d attempts failed: %w", maxRetries+1, lastErr)
}

// cloudPost performs a single POST and returns (body, statusCode, error). The
// reader is rebuilt from body on every call so retries are safe.
func cloudPost(
	ctx context.Context, c *http.Client, url string, headers map[string]string, body []byte,
) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}
	return respBody, resp.StatusCode, nil
}
