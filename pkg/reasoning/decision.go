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

import "fmt"

// HoldDecision returns a ScaleDecision that instructs the scaler to take no
// action this cycle. TargetReplicas is set to zero; the policy enforcer and
// scaler ignore it when Action is "hold".
func HoldDecision(reason string) ScaleDecision {
	return ScaleDecision{
		Action:    "hold",
		Reason:    reason,
		Timestamp: nowMetav1(),
	}
}

// ScaleUpDecision returns a ScaleDecision instructing the scaler to increase
// replicas from current to target. The reason string is annotated with the
// replica delta so on-call SREs can see the magnitude at a glance.
func ScaleUpDecision(current, target int32, confidence float64, reason string) ScaleDecision {
	return ScaleDecision{
		Action:         "scale-up",
		TargetReplicas: target,
		Confidence:     confidence,
		Reason:         fmt.Sprintf("%s (replicas %d → %d)", reason, current, target),
		Timestamp:      nowMetav1(),
	}
}

// ScaleDownDecision returns a ScaleDecision instructing the scaler to decrease
// replicas from current to target.
func ScaleDownDecision(current, target int32, confidence float64, reason string) ScaleDecision {
	return ScaleDecision{
		Action:         "scale-down",
		TargetReplicas: target,
		Confidence:     confidence,
		Reason:         fmt.Sprintf("%s (replicas %d → %d)", reason, current, target),
		Timestamp:      nowMetav1(),
	}
}
