package policy

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStabilizeScaleDown(t *testing.T) {
	now := time.Now()
	spec := baseSpec()
	spec.Policy.ScaleDownStabilizationSeconds = 300
	ago := func(d time.Duration) *metav1.Time { m := metav1.NewTime(now.Add(-d)); return &m }

	// The kind regression: 11 → 2 one minute after scaling up to 11.
	got := StabilizeScaleDown(spec, ago(time.Minute), now, from(11, scaleDown(2)))
	if got.Action != "hold" || got.Guardrail != guardrailDownStabilization ||
		!strings.Contains(got.Reason, "scaled up 1m0s ago; scale-downs wait 300s") ||
		!strings.Contains(got.Reason, "would have scaled to 2 replicas") {
		t.Errorf("scale-down inside the window must hold and say why: %+v", got)
	}

	cases := map[string]struct {
		window int32
		last   *metav1.Time
		cur    int32
	}{
		"window elapsed":             {300, ago(6 * time.Minute), 11},
		"window off":                 {0, ago(time.Minute), 11},
		"never scaled up":            {300, nil, 11},
		"back under lowered maximum": {300, ago(time.Minute), 25},
	}
	for name, c := range cases {
		s := spec
		s.Policy.ScaleDownStabilizationSeconds = c.window
		if got := StabilizeScaleDown(s, c.last, now, from(c.cur, scaleDown(2))); got.Action != "scale-down" {
			t.Errorf("%s: scale-down must pass: %+v", name, got)
		}
	}

	if got := StabilizeScaleDown(spec, ago(time.Minute), now, from(4, scaleUp(8))); got.Action != "scale-up" {
		t.Errorf("scale-ups are never stabilized: %+v", got)
	}
}
