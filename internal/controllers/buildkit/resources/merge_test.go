// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package resources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestApplyMaximums(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maximum      corev1.ResourceList
		desired      []corev1.ResourceRequirements
		want         corev1.ResourceRequirements
		wantModified bool
	}{
		{
			name:         "nil desired requirements",
			maximum:      corev1.ResourceList{},
			desired:      nil,
			want:         corev1.ResourceRequirements{},
			wantModified: false,
		},
		{
			name:         "empty desired requirements",
			maximum:      corev1.ResourceList{},
			desired:      []corev1.ResourceRequirements{},
			want:         corev1.ResourceRequirements{},
			wantModified: false,
		},
		{
			name: "single desired within limits",
			maximum: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			desired: []corev1.ResourceRequirements{
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			wantModified: false,
		},
		{
			name: "limits exceed maximum - should be reduced",
			maximum: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			desired: []corev1.ResourceRequirements{
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			wantModified: true,
		},
		{
			name: "requests exceed maximum - should be reduced",
			maximum: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			desired: []corev1.ResourceRequirements{
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("6Gi"),
					},
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
			},
			wantModified: true,
		},
		{
			name: "missing limits filled with maximum",
			maximum: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			desired: []corev1.ResourceRequirements{
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"),
					},
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("500m"),
				},
			},
			wantModified: true,
		},
		{
			name: "nil limits and requests in desired",
			maximum: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			desired: []corev1.ResourceRequirements{
				{
					Limits:   nil,
					Requests: nil,
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: nil,
			},
			wantModified: true,
		},
		{
			name: "multiple desired requirements - later takes precedence",
			maximum: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
			desired: []corev1.ResourceRequirements{
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
				},
				{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("3Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1.5Gi"),
					},
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("3Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1.5Gi"),
				},
			},
			wantModified: false,
		},
		{
			name: "multiple desired with some exceeding limits",
			maximum: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			desired: []corev1.ResourceRequirements{
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
				},
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			wantModified: true,
		},
		{
			name: "partial overlap between maximum and desired resources",
			maximum: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("2"),
				corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
			},
			desired: []corev1.ResourceRequirements{
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:              resource.MustParse("1"),
					corev1.ResourceMemory:           resource.MustParse("4Gi"),
					corev1.ResourceEphemeralStorage: resource.MustParse("100Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
			},
			wantModified: true,
		},
		{
			name:    "requests exceeding limits should be reduced to limits",
			maximum: corev1.ResourceList{},
			desired: []corev1.ResourceRequirements{
				{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
			},
			wantModified: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, modified := ApplyMaximums(tt.maximum, tt.desired...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantModified, modified)

			// We can also test WithMaximums and ExceedsMaximums here with the same inputs
			assert.Equal(t, tt.want, WithMaximums(tt.maximum, tt.desired...))
			assert.Equal(t, tt.wantModified, ExceedsMaximums(tt.maximum, tt.desired...))
		})
	}
}
