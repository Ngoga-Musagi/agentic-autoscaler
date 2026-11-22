package main

import (
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// seriesLabels gathers reg and returns, per metric family name, the sorted
// label names on its first sample.
func seriesLabels(t *testing.T, reg *prometheus.Registry) map[string][]string {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string][]string{}
	for _, mf := range families {
		var names []string
		for _, l := range mf.GetMetric()[0].GetLabel() {
			names = append(names, l.GetName())
		}
		sort.Strings(names)
		out[mf.GetName()] = names
	}
	return out
}

// The default style must keep the names and labels the operator's default
// PromQL reads; the prefixed style must not, so a CR has to override them.
func TestNewMetrics_NamingStyles(t *testing.T) {
	cases := []struct {
		prefix string
		want   map[string][]string
	}{
		{"", map[string][]string{
			"http_request_duration_seconds": {"service"},
			"http_requests_total":           {"service", "status"},
		}},
		{"shop", map[string][]string{
			"shop_request_seconds": {"app"},
			"shop_requests_total":  {"app", "code"},
		}},
	}
	for _, tc := range cases {
		reg := prometheus.NewRegistry()
		m := newMetrics(reg, "checkout", tc.prefix)
		m.observe(200, 0)

		got := seriesLabels(t, reg)
		if len(got) != len(tc.want) {
			t.Fatalf("prefix %q: want families %v, got %v", tc.prefix, tc.want, got)
		}
		for name, labels := range tc.want {
			if g := got[name]; len(g) != len(labels) || (len(g) > 0 && g[0] != labels[0]) ||
				(len(g) > 1 && g[1] != labels[1]) {
				t.Errorf("prefix %q: %s labels: want %v, got %v", tc.prefix, name, labels, g)
			}
		}
	}
}
