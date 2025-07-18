// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package merge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/seatgeek/buildkit-operator/internal/merge"
)

func TestObjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		base      *corev1.Pod
		overrides []corev1.Pod
		want      *corev1.Pod
	}{
		{
			name: "single override merges labels",
			base: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						"app":     "test",
						"version": "1.0",
					},
				},
			},
			overrides: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"version": "2.0",
							"env":     "prod",
						},
					},
				},
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						"app":     "test",
						"version": "2.0",
						"env":     "prod",
					},
				},
			},
		},
		{
			name: "multiple overrides applied in sequence",
			base: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						"app": "test",
					},
				},
			},
			overrides: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"version": "1.0",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"version": "2.0",
							"env":     "prod",
						},
					},
				},
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						"app":     "test",
						"version": "2.0",
						"env":     "prod",
					},
				},
			},
		},
		{
			name: "container merging by name",
			base: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.0",
						},
						{
							Name:  "sidecar",
							Image: "busybox:1.0",
						},
					},
				},
			},
			overrides: []corev1.Pod{
				{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "sidecar",
								Image: "busybox:2.0",
							},
							{
								Name:  "logger",
								Image: "fluentd:1.0",
							},
						},
					},
				},
			},
			want: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.0",
						},
						{
							Name:  "sidecar",
							Image: "busybox:2.0",
						},
						{
							Name:  "logger",
							Image: "fluentd:1.0",
						},
					},
				},
			},
		},
		{
			name: "empty overrides is no-op",
			base: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						"app": "test",
					},
				},
			},
			overrides: []corev1.Pod{},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						"app": "test",
					},
				},
			},
		},
		{
			name: "annotations are merged",
			base: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
						"prometheus.io/port":   "8080",
					},
				},
			},
			overrides: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"prometheus.io/port": "9090",
							"prometheus.io/path": "/metrics",
						},
					},
				},
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
						"prometheus.io/port":   "9090",
						"prometheus.io/path":   "/metrics",
					},
				},
			},
		},
		{
			name: "spec fields are merged",
			base: &corev1.Pod{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyAlways,
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.0",
						},
					},
				},
			},
			overrides: []corev1.Pod{
				{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyOnFailure,
						NodeSelector: map[string]string{
							"disk": "ssd",
						},
						Containers: []corev1.Container{
							{
								Name:  "app",
								Image: "nginx:1.0",
							},
						},
					},
				},
			},
			want: &corev1.Pod{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					NodeSelector: map[string]string{
						"disk": "ssd",
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.0",
						},
					},
				},
			},
		},
		{
			name: "empty base gets overridden",
			base: &corev1.Pod{},
			overrides: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pod",
						Labels: map[string]string{
							"app": "test",
						},
					},
				},
			},
			want: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						"app": "test",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := merge.Objects(tt.base, tt.overrides...)
			require.NoError(t, err)
			assert.Equal(t, tt.want, tt.base)
		})
	}
}

func TestObjects_WithDifferentTypes(t *testing.T) {
	t.Parallel()

	// Test with ConfigMap
	base := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
		Data: map[string]string{
			"key1": "value1",
		},
	}

	override := corev1.ConfigMap{
		Data: map[string]string{
			"key2": "value2",
		},
	}

	err := merge.Objects(base, override)
	require.NoError(t, err)

	expected := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
		Data: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}

	assert.Equal(t, expected, base)
}

func TestObjects_DoesNotModifyOverrides(t *testing.T) {
	t.Parallel()

	base := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod",
			Labels: map[string]string{
				"app": "test",
			},
		},
	}

	override1 := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"version": "1.0",
			},
		},
	}

	override2 := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"env": "prod",
			},
		},
	}

	// Create copies to compare against later
	override1Copy := override1.DeepCopy()
	override2Copy := override2.DeepCopy()

	// Call the function
	err := merge.Objects(base, override1, override2)
	require.NoError(t, err)

	// Verify overrides were not modified
	assert.Equal(t, override1Copy, &override1, "override1 should not be modified")
	assert.Equal(t, override2Copy, &override2, "override2 should not be modified")
}
