package queryapi

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
)

func TestSummarize_ExposesGovernanceState(t *testing.T) {
	aa := &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout-autoscaler"},
		Spec:       scalingv1alpha1.AgenticAutoscalerSpec{TargetDeployment: "checkout"},
		Status: scalingv1alpha1.AgenticAutoscalerStatus{
			LastGuardrail:       "approval-required",
			PendingApproval:     &scalingv1alpha1.PendingApproval{TargetReplicas: 18},
			RecommendedReplicas: 6,
		},
	}
	s := summarize(aa)
	if s.LastGuardrail != "approval-required" || s.PendingApprovalReplicas != 18 || s.RecommendedReplicas != 6 {
		t.Errorf("summary: %+v", s)
	}
	aa.Status.PendingApproval = nil
	if summarize(aa).PendingApprovalReplicas != 0 {
		t.Error("no pending approval must summarize as 0")
	}
}
