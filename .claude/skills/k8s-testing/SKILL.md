---
name: k8s-testing
description: Testing patterns for the agentic-autoscaler operator. Use when writing tests in _test.go files, setting up envtest suites, mocking Prometheus or Loki, creating AgenticAutoscaler fixtures, or verifying reconcile logic. Covers envtest setup, Gomega matchers, fake signal sources, and integration test structure.
allowed-tools: Read, Grep, Glob
---

# Kubernetes operator testing

## Test pyramid for this project

```
Unit tests (pkg/signals, pkg/fusion, pkg/reasoning, pkg/policy)
  ↓ no cluster, no network, pure Go
Integration tests (internal/controller + envtest)
  ↓ real Kubernetes API server, fake etcd, no network calls
Load/scenario tests (k6 against staging)
  ↓ real cluster, real signals, validates proactive scaling
```

## envtest suite setup

```go
// internal/controller/suite_test.go

var (
    cfg       *rest.Config
    k8sClient client.Client
    testEnv   *envtest.Environment
    ctx       context.Context
    cancel    context.CancelFunc
)

func TestControllers(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
    ctx, cancel = context.WithCancel(context.TODO())

    testEnv = &envtest.Environment{
        CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
        ErrorIfCRDPathMissing: true,
    }
    var err error
    cfg, err = testEnv.Start()
    Expect(err).NotTo(HaveOccurred())

    err = scalingv1alpha1.AddToScheme(scheme.Scheme)
    Expect(err).NotTo(HaveOccurred())

    k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
    Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
    cancel()
    Expect(testEnv.Stop()).To(Succeed())
})
```

## Integration test — reconcile loop

```go
// internal/controller/autoscaler_controller_test.go

var _ = Describe("AgenticAutoscaler controller", func() {
    Context("with a deployment and a dry-run AgenticAutoscaler", func() {
        It("should log the scale decision without patching replicas", func() {
            ns := "test-" + randString(6)
            createNamespace(ns)

            deploy := makeDeployment("payment-service", ns, 3)
            Expect(k8sClient.Create(ctx, deploy)).To(Succeed())

            aa := makeAgenticAutoscaler("payment-service", ns, AgenticAutoscalerSpec{
                TargetDeployment: "payment-service",
                MinReplicas:      2,
                MaxReplicas:      10,
                DryRun:           true,
                ReasoningBackend: ReasoningBackendConfig{Provider: "rule-based"},
            })
            Expect(k8sClient.Create(ctx, aa)).To(Succeed())

            // inject a fake critical signal via the fake signal source
            fakeSignals.InjectPattern("payment-service", ns, "connection-pool-exhausted")

            // trigger reconcile
            reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{
                Name: "payment-service", Namespace: ns,
            }})

            // replicas should NOT have changed (dry-run)
            updated := &appsv1.Deployment{}
            Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "payment-service", Namespace: ns}, updated)).To(Succeed())
            Expect(*updated.Spec.Replicas).To(Equal(int32(3)))

            // status should have been updated with the decision
            updatedAA := &scalingv1alpha1.AgenticAutoscaler{}
            Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "payment-service", Namespace: ns}, updatedAA)).To(Succeed())
            Expect(updatedAA.Status.LastDecisionReason).To(ContainSubstring("connection-pool-exhausted"))
        })
    })
})
```

## Fake signal source — inject patterns without real Prometheus/Loki

```go
// internal/controller/fake_signals_test.go

type FakeSignalSource struct {
    mu       sync.Mutex
    patterns map[string][]string  // key: "name/namespace"
}

func (f *FakeSignalSource) Collect(ctx context.Context, target string, ns string) (signals.SystemSnapshot, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    key := target + "/" + ns
    injected := f.patterns[key]

    snap := signals.SystemSnapshot{
        Timestamp: time.Now(),
        Metrics:   signals.MetricSnapshot{P99LatencyMS: 3500, ErrorRatePerSec: 0.02},
    }
    for _, p := range injected {
        snap.RecentLogs = append(snap.RecentLogs, signals.LogEntry{
            Timestamp: time.Now(),
            Line:      p,
        })
    }
    return snap, nil
}

func (f *FakeSignalSource) InjectPattern(target, ns, pattern string) {
    f.mu.Lock()
    defer f.mu.Unlock()
    key := target + "/" + ns
    f.patterns[key] = append(f.patterns[key], pattern)
}
```

## Unit test — reasoning engine

```go
// pkg/reasoning/rule_agent_test.go

func TestRuleAgent_ScaleUpOnCriticalPattern(t *testing.T) {
    agent := &RuleAgent{}
    sig := fusion.FusedSignal{
        AnomalyScore: 0.9,
        MatchedPatterns: []fusion.NamedPattern{
            {Name: "connection-pool-exhausted", Severity: "critical", Matched: true, Count: 12},
        },
        Snapshot: signals.SystemSnapshot{
            Metrics: signals.MetricSnapshot{P99LatencyMS: 2500},
        },
    }
    decision, err := agent.Decide(context.Background(), sig, PolicyHints{
        CurrentReplicas: 3, MinReplicas: 2, MaxReplicas: 10,
    })
    assert.NoError(t, err)
    assert.Equal(t, ScaleUp, decision.Direction)
    assert.Greater(t, decision.TargetReplicas, int32(3))
    assert.Greater(t, decision.Confidence, float64(0.8))
    assert.Contains(t, decision.Explanation, "connection-pool-exhausted")
}
```

## Test helper — fixture builder

```go
func makeAgenticAutoscaler(name, ns string, spec AgenticAutoscalerSpec) *scalingv1alpha1.AgenticAutoscaler {
    return &scalingv1alpha1.AgenticAutoscaler{
        ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
        Spec:       spec,
    }
}

func makeDeployment(name, ns string, replicas int32) *appsv1.Deployment {
    return &appsv1.Deployment{
        ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
        Spec: appsv1.DeploymentSpec{
            Replicas: &replicas,
            Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
            Template: corev1.PodTemplateSpec{
                ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
                Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
            },
        },
    }
}
```

## Anti-patterns

- Never use `time.Sleep` in tests — use `Eventually(func() bool {...}, timeout, interval).Should(BeTrue())`
- Never hard-code port numbers in envtest — let the framework assign them
- Never test reconcile by calling internal functions directly — call `reconciler.Reconcile()` to exercise the full path
- Never skip cleanup — always call `k8sClient.Delete` or use `DeferCleanup`
