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
	"strings"
	"text/template"

	"github.com/Ngoga-Musagi/agentic-autoscaler/pkg/fusion"
)

// SystemPrompt is the static system message sent to every AI provider.
// It instructs the model to return ONLY raw JSON — no prose, no markdown,
// no code fences — so the response can be parsed without stripping.
const SystemPrompt = `You are a Kubernetes autoscaling agent. Your sole job is to analyze the system health signals provided and return a precise scaling decision.

Rules you must follow without exception:
1. Respond ONLY with a single valid JSON object. No explanation, no markdown, no code blocks.
2. The JSON must contain exactly these four keys: action, targetReplicas, confidence, reason.
3. "action" must be exactly one of: "scale-up", "scale-down", or "hold".
4. "targetReplicas" must be an integer within the bounds stated in the user message.
5. "confidence" must be a float between 0.0 and 1.0 reflecting your certainty.
6. "reason" must be a single sentence written for on-call SREs explaining the decision.

Example of the only acceptable response format:
{"action":"scale-up","targetReplicas":8,"confidence":0.9,"reason":"Connection pool exhausted with p99 latency at 2.4s indicates the current replica count cannot handle load."}`

// userPromptTmpl is the dynamic portion of the prompt, rendered per reconcile cycle.
var userPromptTmpl = template.Must(template.New("user").Parse(`Deployment: {{.TargetDeployment}}
Current replicas: {{.CurrentReplicas}} (allowed range: {{.MinReplicas}}–{{.MaxReplicas}})

Metrics (past 2 minutes):
  Latency p99:     {{printf "%.1f" .LatencyP99Ms}}ms
  HTTP error rate: {{printf "%.2f" .ErrorRatePct}}%
  CPU utilisation: {{printf "%.1f" .CPUUtilPct}}%
  Requests/sec:    {{printf "%.1f" .RequestsPerSec}}
{{if .Patterns}}
Log patterns detected:{{range .Patterns}}
  - {{.Name}} ({{.Severity}}): {{.Count}} occurrence(s){{end}}
{{- else}}
Log patterns detected: none
{{- end}}
Severity score: {{printf "%.2f" .SeverityScore}} (0.00 = healthy, 1.00 = critical)

Respond with ONLY valid JSON. targetReplicas must be in [{{.MinReplicas}}, {{.MaxReplicas}}]:
{"action":"scale-up|scale-down|hold","targetReplicas":N,"confidence":0.0-1.0,"reason":"one sentence for on-call SREs"}`))

// promptData is the template context assembled from Config and FusedSignal.
type promptData struct {
	TargetDeployment string
	CurrentReplicas  int32
	MinReplicas      int32
	MaxReplicas      int32
	LatencyP99Ms     float64
	ErrorRatePct     float64
	CPUUtilPct       float64
	RequestsPerSec   float64
	Patterns         []fusion.PatternMatch
	SeverityScore    float64
}

// BuildPrompt renders the user-turn message for the AI provider from the
// current Config and FusedSignal. The static system prompt is in SystemPrompt.
func BuildPrompt(cfg Config, signal fusion.FusedSignal) string {
	data := promptData{
		TargetDeployment: cfg.TargetDeployment,
		CurrentReplicas:  cfg.CurrentReplicas,
		MinReplicas:      cfg.MinReplicas,
		MaxReplicas:      cfg.MaxReplicas,
		LatencyP99Ms:     signal.Snapshot.Metrics.LatencyP99Ms,
		ErrorRatePct:     signal.Snapshot.Metrics.ErrorRatePct,
		CPUUtilPct:       signal.Snapshot.Metrics.CPUUtilPct,
		RequestsPerSec:   signal.Snapshot.Metrics.RequestsPerSec,
		Patterns:         signal.MatchedPatterns,
		SeverityScore:    signal.SeverityScore,
	}
	var buf strings.Builder
	// Template is compiled at init; Execute can only fail on I/O errors from
	// the writer, which strings.Builder never produces.
	_ = userPromptTmpl.Execute(&buf, data)
	return buf.String()
}
