// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package resources

import corev1 "k8s.io/api/core/v1"

func Merge(desired *corev1.ResourceRequirements, defaults corev1.ResourceRequirements) corev1.ResourceRequirements {
	if desired == nil {
		return defaults
	}

	merged := *defaults.DeepCopy()

	// Desired limits only get merged if they are less than (or equal to) any corresponding default.
	for key, value := range desired.Limits {
		if defaultValue, exists := merged.Limits[key]; !exists || value.Cmp(defaultValue) <= 0 {
			// Initialize limits map if needed
			if merged.Limits == nil {
				merged.Limits = make(corev1.ResourceList)
			}
			merged.Limits[key] = value
		}
	}

	// Desired requests are always merged, but they may be reduced to satisfy the limits.
	for key, value := range desired.Requests {
		if defaultValue, exists := merged.Limits[key]; exists && value.Cmp(defaultValue) > 0 {
			// The request exceeds the limit
			// Initialize requests map if needed
			if merged.Requests == nil {
				merged.Requests = make(corev1.ResourceList)
			}
			merged.Requests[key] = defaultValue
		} else {
			// Initialize requests map if needed
			if merged.Requests == nil {
				merged.Requests = make(corev1.ResourceList)
			}
			merged.Requests[key] = value
		}
	}

	return merged
}
