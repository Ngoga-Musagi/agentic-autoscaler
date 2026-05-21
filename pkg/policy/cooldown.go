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

package policy

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CooldownChecker determines whether the cooldown window from the last scale
// action is still active. It is a separate type so tests can inject an
// arbitrary clock via the now parameter rather than sleeping.
type CooldownChecker struct{}

// IsActive reports whether the cooldown window is still open.
//
// now is passed in rather than calling time.Now() internally so that unit
// tests can assert timing behaviour without real sleeps.
// Returns false when lastScaleTime is nil (never scaled → no cooldown).
func (c *CooldownChecker) IsActive(lastScaleTime *metav1.Time, cooldownSeconds int32, now time.Time) bool {
	if lastScaleTime == nil || lastScaleTime.IsZero() {
		return false
	}
	return now.Sub(lastScaleTime.Time) < time.Duration(cooldownSeconds)*time.Second
}
