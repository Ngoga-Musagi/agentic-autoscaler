package v1alpha1

import (
	"encoding/json"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// buildSample constructs a fully-populated AgenticAutoscaler for round-trip tests.
func buildSample() AgenticAutoscaler {
	return AgenticAutoscaler{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "scaling.autoscaler.io/v1alpha1",
			Kind:       "AgenticAutoscaler",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payment-service-autoscaler",
			Namespace: "production",
		},
		Spec: AgenticAutoscalerSpec{
			TargetDeployment: "payment-service",
			Namespace:        "production",
			PrometheusURL:    "http://prometheus-operated.monitoring:9090",
			LogSource: LogSourceConfig{
				Type: "loki",
				Loki: &LokiConfig{
					URL:   "http://loki.monitoring:3100",
					Query: `{namespace="production", app="payment-service"}`,
				},
			},
			MinReplicas:     3,
			MaxReplicas:     15,
			CooldownSeconds: 180,
			DryRun:          true,
			AIProvider: AIProviderConfig{
				Provider:  "anthropic",
				SecretRef: "ai-provider-secret",
			},
			HPACoexistence: HPACoexistence{
				Mode:         "calibrated",
				HPAName:      "payment-service-hpa",
				HPANamespace: "production",
			},
			Observability: ObservabilityConfig{
				GrafanaURL: "http://grafana.monitoring:3000",
				SecretRef:  "grafana-api-secret",
			},
		},
		Status: AgenticAutoscalerStatus{
			CurrentReplicas:      5,
			LastDecisionReason:   "connection pool exhausted; scaled up 50%",
			HPACoexistenceStatus: "calibrated",
			ObservedGeneration:   2,
		},
	}
}

func TestJSONRoundTrip(t *testing.T) {
	original := buildSample()

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var got AgenticAutoscaler
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// --- TypeMeta ---
	assertEqual(t, "TypeMeta.APIVersion", original.APIVersion, got.APIVersion)
	assertEqual(t, "TypeMeta.Kind", original.Kind, got.Kind)

	// --- ObjectMeta ---
	assertEqual(t, "Name", original.Name, got.Name)
	assertEqual(t, "Namespace", original.Namespace, got.Namespace)

	// --- Spec: top-level scalars ---
	assertEqual(t, "Spec.TargetDeployment", original.Spec.TargetDeployment, got.Spec.TargetDeployment)
	assertEqual(t, "Spec.Namespace", original.Spec.Namespace, got.Spec.Namespace)
	assertEqual(t, "Spec.PrometheusURL", original.Spec.PrometheusURL, got.Spec.PrometheusURL)
	assertInt32(t, "Spec.MinReplicas", original.Spec.MinReplicas, got.Spec.MinReplicas)
	assertInt32(t, "Spec.MaxReplicas", original.Spec.MaxReplicas, got.Spec.MaxReplicas)
	assertInt32(t, "Spec.CooldownSeconds", original.Spec.CooldownSeconds, got.Spec.CooldownSeconds)
	assertBool(t, "Spec.DryRun", original.Spec.DryRun, got.Spec.DryRun)

	// --- Spec.LogSource ---
	assertEqual(t, "Spec.LogSource.Type", original.Spec.LogSource.Type, got.Spec.LogSource.Type)
	if got.Spec.LogSource.Loki == nil {
		t.Fatal("Spec.LogSource.Loki: got nil, want non-nil")
	}
	assertEqual(t, "Spec.LogSource.Loki.URL", original.Spec.LogSource.Loki.URL, got.Spec.LogSource.Loki.URL)
	assertEqual(t, "Spec.LogSource.Loki.Query", original.Spec.LogSource.Loki.Query, got.Spec.LogSource.Loki.Query)
	if got.Spec.LogSource.Kafka != nil {
		t.Error("Spec.LogSource.Kafka: want nil for loki-type source")
	}

	// --- Spec.AIProvider ---
	assertEqual(t, "Spec.AIProvider.Provider", original.Spec.AIProvider.Provider, got.Spec.AIProvider.Provider)
	assertEqual(t, "Spec.AIProvider.SecretRef", original.Spec.AIProvider.SecretRef, got.Spec.AIProvider.SecretRef)

	// --- Spec.HPACoexistence ---
	assertEqual(t, "Spec.HPACoexistence.Mode", original.Spec.HPACoexistence.Mode, got.Spec.HPACoexistence.Mode)
	assertEqual(t, "Spec.HPACoexistence.HPAName", original.Spec.HPACoexistence.HPAName, got.Spec.HPACoexistence.HPAName)
	assertEqual(t, "Spec.HPACoexistence.HPANamespace", original.Spec.HPACoexistence.HPANamespace, got.Spec.HPACoexistence.HPANamespace)

	// --- Spec.Observability ---
	assertEqual(t, "Spec.Observability.GrafanaURL", original.Spec.Observability.GrafanaURL, got.Spec.Observability.GrafanaURL)
	assertEqual(t, "Spec.Observability.SecretRef", original.Spec.Observability.SecretRef, got.Spec.Observability.SecretRef)

	// --- Status ---
	assertInt32(t, "Status.CurrentReplicas", original.Status.CurrentReplicas, got.Status.CurrentReplicas)
	assertEqual(t, "Status.LastDecisionReason", original.Status.LastDecisionReason, got.Status.LastDecisionReason)
	assertEqual(t, "Status.HPACoexistenceStatus", original.Status.HPACoexistenceStatus, got.Status.HPACoexistenceStatus)
	assertInt64(t, "Status.ObservedGeneration", original.Status.ObservedGeneration, got.Status.ObservedGeneration)
}

func TestJSONRoundTripKafkaLogSource(t *testing.T) {
	original := buildSample()
	original.Spec.LogSource = LogSourceConfig{
		Type: "kafka",
		Kafka: &KafkaConfig{
			Brokers: []string{"broker-0.kafka:9092", "broker-1.kafka:9092"},
			Topic:   "app-logs",
			GroupID: "agentic-autoscaler",
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got AgenticAutoscaler
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	assertEqual(t, "Spec.LogSource.Type", "kafka", got.Spec.LogSource.Type)
	if got.Spec.LogSource.Kafka == nil {
		t.Fatal("Spec.LogSource.Kafka: got nil, want non-nil")
	}
	assertEqual(t, "Spec.LogSource.Kafka.Topic", "app-logs", got.Spec.LogSource.Kafka.Topic)
	assertEqual(t, "Spec.LogSource.Kafka.GroupID", "agentic-autoscaler", got.Spec.LogSource.Kafka.GroupID)
	if len(got.Spec.LogSource.Kafka.Brokers) != 2 {
		t.Fatalf("Spec.LogSource.Kafka.Brokers: got %d items, want 2", len(got.Spec.LogSource.Kafka.Brokers))
	}
	assertEqual(t, "Spec.LogSource.Kafka.Brokers[0]", "broker-0.kafka:9092", got.Spec.LogSource.Kafka.Brokers[0])
	if got.Spec.LogSource.Loki != nil {
		t.Error("Spec.LogSource.Loki: want nil for kafka-type source")
	}
}

func TestJSONRoundTripLastScaleTime(t *testing.T) {
	original := buildSample()
	ts := metav1.Now()
	original.Status.LastScaleTime = &ts

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got AgenticAutoscaler
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Status.LastScaleTime == nil {
		t.Fatal("Status.LastScaleTime: got nil, want non-nil")
	}
	// metav1.Time marshals to RFC3339 second precision; compare Unix seconds.
	if got.Status.LastScaleTime.Unix() != ts.Unix() {
		t.Errorf("Status.LastScaleTime: got %v, want %v", got.Status.LastScaleTime, ts)
	}
}

func TestSampleYAMLDeserializes(t *testing.T) {
	raw, err := os.ReadFile("../../config/samples/payment-service-autoscaler.yaml")
	if err != nil {
		t.Fatalf("reading sample file: %v", err)
	}

	var aa AgenticAutoscaler
	if err := yaml.Unmarshal(raw, &aa); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	// --- identity ---
	assertEqual(t, "Name", "payment-service-autoscaler", aa.Name)
	assertEqual(t, "Namespace", "production", aa.Namespace)

	// --- required spec fields ---
	assertEqual(t, "Spec.TargetDeployment", "payment-service", aa.Spec.TargetDeployment)
	assertEqual(t, "Spec.PrometheusURL", "http://prometheus-operated.monitoring:9090", aa.Spec.PrometheusURL)
	assertInt32(t, "Spec.MinReplicas", 3, aa.Spec.MinReplicas)
	assertInt32(t, "Spec.MaxReplicas", 15, aa.Spec.MaxReplicas)
	assertInt32(t, "Spec.CooldownSeconds", 180, aa.Spec.CooldownSeconds)
	assertBool(t, "Spec.DryRun", true, aa.Spec.DryRun)

	// --- log source ---
	assertEqual(t, "Spec.LogSource.Type", "loki", aa.Spec.LogSource.Type)
	if aa.Spec.LogSource.Loki == nil {
		t.Fatal("Spec.LogSource.Loki: got nil, want non-nil")
	}
	assertEqual(t, "Spec.LogSource.Loki.URL", "http://loki.monitoring:3100", aa.Spec.LogSource.Loki.URL)

	// --- AI provider ---
	assertEqual(t, "Spec.AIProvider.Provider", "anthropic", aa.Spec.AIProvider.Provider)
	assertEqual(t, "Spec.AIProvider.SecretRef", "ai-provider-secret", aa.Spec.AIProvider.SecretRef)

	// --- HPA coexistence ---
	assertEqual(t, "Spec.HPACoexistence.Mode", "calibrated", aa.Spec.HPACoexistence.Mode)
	assertEqual(t, "Spec.HPACoexistence.HPAName", "payment-service-hpa", aa.Spec.HPACoexistence.HPAName)
	assertEqual(t, "Spec.HPACoexistence.HPANamespace", "production", aa.Spec.HPACoexistence.HPANamespace)

	// --- observability ---
	assertEqual(t, "Spec.Observability.GrafanaURL", "http://grafana.monitoring:3000", aa.Spec.Observability.GrafanaURL)
	assertEqual(t, "Spec.Observability.SecretRef", "grafana-api-secret", aa.Spec.Observability.SecretRef)
}

// assertEqual fails the test if want != got.
func assertEqual(t *testing.T, field, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %q, got %q", field, want, got)
	}
}

func assertInt32(t *testing.T, field string, want, got int32) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %d, got %d", field, want, got)
	}
}

func assertInt64(t *testing.T, field string, want, got int64) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %d, got %d", field, want, got)
	}
}

func assertBool(t *testing.T, field string, want, got bool) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %v, got %v", field, want, got)
	}
}
