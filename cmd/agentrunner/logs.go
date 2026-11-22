package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// The runner's failure-mode log lines. Each is crafted to match a log pattern
// the agent-runner CRs declare (and, with spec.agentic set, the operator's
// built-in agentic patterns), so the demo genuinely exercises Loki → fusion.
// logs_test.go verifies every line against those patterns.

func lineRateLimited(attempt int) string {
	return fmt.Sprintf("WARN LLM rate limited (HTTP 429) on attempt %d, backing off", attempt)
}

func lineToolTimeout(tool string, after time.Duration) string {
	return fmt.Sprintf("ERROR tool %s timed out after %s", tool, after)
}

func lineContextOverflow() string {
	return "WARN context length exceeded: prompt 131072 tokens > 128000 limit, truncating history"
}

func linePoolSaturated(workers int, waited time.Duration) string {
	return fmt.Sprintf("WARN agent worker pool saturated: all %d workers busy, task queued %dms",
		workers, waited.Milliseconds())
}

func lineQueueFull(queued int64) string {
	return fmt.Sprintf("ERROR task queue full (%d queued), rejecting task", queued)
}

// throttle writes a given kind of line at most once per interval. At tens of
// tasks per second an unthrottled line per task would flood Loki; one line per
// interval is enough to keep a fresh match in the operator's 2-minute window.
type throttle struct {
	mu    sync.Mutex
	every time.Duration
	last  map[string]time.Time
}

func newThrottle(every time.Duration) *throttle {
	return &throttle{every: every, last: map[string]time.Time{}}
}

func (t *throttle) println(kind, line string) {
	t.mu.Lock()
	now := time.Now()
	if now.Sub(t.last[kind]) < t.every {
		t.mu.Unlock()
		return
	}
	t.last[kind] = now
	t.mu.Unlock()
	log.Println(line)
}
