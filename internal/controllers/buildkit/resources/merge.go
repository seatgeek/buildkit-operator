// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package resources

import corev1 "k8s.io/api/core/v1"

// ApplyMaximums checks the desired resource requirements against a maximum set of limits.
// Any desired requests or limits that exceed the maximum limits are reduced to the maximum.
// Any requests that exceed the maximum limits are also reduced to the maximum.
// Any missing limits are filled in with the maximum values.
// Multiple desired resource requirements can be provided, and they will be merged together (with the later ones taking precedence).
// The function will return the merged resource requirements and a boolean indicating if any modifications were made.
func ApplyMaximums(maximum corev1.ResourceList, desired ...corev1.ResourceRequirements) (result corev1.ResourceRequirements, modified bool) {
	// Merge additional desired resource requirements (later ones take precedence)
	for _, current := range desired {
		// Merge limits (later ones take precedence)
		if current.Limits != nil {
			if result.Limits == nil {
				result.Limits = make(corev1.ResourceList)
			}
			for key, value := range current.Limits {
				result.Limits[key] = value
			}
		}

		// Merge requests (later ones take precedence)
		if current.Requests != nil {
			if result.Requests == nil {
				result.Requests = make(corev1.ResourceList)
			}
			for key, value := range current.Requests {
				result.Requests[key] = value
			}
		}
	}

	// Fill in missing limits with maximum values
	for key, maxValue := range maximum {
		if _, exists := result.Limits[key]; !exists {
			if result.Limits == nil {
				result.Limits = make(corev1.ResourceList)
			}
			result.Limits[key] = maxValue
			modified = true
		}
	}

	// Check and reduce limits that exceed maximum
	for key, value := range result.Limits {
		if maxValue, exists := maximum[key]; exists && value.Cmp(maxValue) > 0 {
			result.Limits[key] = maxValue
			modified = true
		}
	}

	// Ensure requests do not exceed their own limits
	// Since limits are already capped at maximum, this also ensures requests don't exceed maximum
	if result.Requests != nil && result.Limits != nil {
		for key, requestValue := range result.Requests {
			if limitValue, exists := result.Limits[key]; exists && requestValue.Cmp(limitValue) > 0 {
				result.Requests[key] = limitValue
				modified = true
			}
		}
	}

	return result, modified
}

// WithMaximums applies a maximum set of resource limits to the desired resource requirements.
// It works identically to ApplyMaximums, but only returns the merged resource requirements without the modification flag.
func WithMaximums(maximum corev1.ResourceList, desired ...corev1.ResourceRequirements) corev1.ResourceRequirements {
	result, _ := ApplyMaximums(maximum, desired...)
	return result
}

// ExceedsMaximums checks if any of the desired resource requirements exceed the specified maximum limits.
// It works identically to ApplyMaximums, but only returns a boolean indicating if any modifications were made.
func ExceedsMaximums(maximum corev1.ResourceList, desired ...corev1.ResourceRequirements) bool {
	_, modified := ApplyMaximums(maximum, desired...)
	return modified
}
