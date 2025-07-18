// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"testing"

	autogold "github.com/hexops/autogold/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

func TestBuilder_BuildPod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		buildkit *v1alpha1.Buildkit
		template *v1alpha1.BuildkitTemplate
		wantErr  string
	}{
		{
			name: "template not found",
			buildkit: &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: "nonexistent-template",
				},
			},
			template: nil, // Not added to fake client
			wantErr:  "not found",
		},
		{
			name: "vanilla happy path",
			buildkit: &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: "test-template",
				},
			},
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					Port: 1234,
					PodTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "buildkit",
									Image: "moby/buildkit:latest",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("100m"),
											corev1.ResourceMemory: resource.MustParse("128Mi"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1000m"),
											corev1.ResourceMemory: resource.MustParse("1Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "with config map",
			buildkit: &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: "test-template",
				},
			},
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					Port:          1234,
					BuildkitdToml: "[worker.oci]\n  enabled = true\n",
					PodTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "buildkit",
									Image: "moby/buildkit:latest",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("100m"),
											corev1.ResourceMemory: resource.MustParse("128Mi"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1000m"),
											corev1.ResourceMemory: resource.MustParse("1Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "full customization",
			buildkit: &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: "test-template",
					Resources: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("200m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
					Labels: map[string]string{
						"app.kubernetes.io/name":    "custom-buildkit", // Conflicts with default
						"app.kubernetes.io/version": "v1.0.0",
					},
					Annotations: map[string]string{
						"example.com/custom": "value",
					},
				},
			},
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					Port:          8080,
					BuildkitdToml: "[worker.oci]\n  enabled = true\n",
					PodTemplate: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app.kubernetes.io/name":      "template-buildkit", // Will be overridden
								"app.kubernetes.io/component": "builder",
							},
							Annotations: map[string]string{
								"template.example.com/config": "enabled",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "buildkit",
									Image: "custom/buildkit:v1.2.3",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("100m"),
											corev1.ResourceMemory: resource.MustParse("128Mi"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1000m"),
											corev1.ResourceMemory: resource.MustParse("1Gi"),
										},
									},
								},
							},
							NodeSelector: map[string]string{
								"kubernetes.io/arch": "amd64",
							},
							Tolerations: []corev1.Toleration{
								{
									Key:      "node-role.kubernetes.io/master",
									Operator: corev1.TolerationOpExists,
									Effect:   corev1.TaintEffectNoSchedule,
								},
							},
							SecurityContext: &corev1.PodSecurityContext{
								RunAsNonRoot: ptr.To(true),
								RunAsUser:    ptr.To(int64(1000)),
							},
						},
					},
				},
			},
		},
		{
			name: "resource requirements only",
			buildkit: &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: "test-template",
					Resources: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("800m"),
							corev1.ResourceMemory: resource.MustParse("800Mi"),
						},
					},
				},
			},
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					Port:          1234,
					BuildkitdToml: "",
					PodTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "buildkit",
									Image: "moby/buildkit:latest",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("100m"),
											corev1.ResourceMemory: resource.MustParse("128Mi"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1000m"),
											corev1.ResourceMemory: resource.MustParse("1Gi"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client
			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			var objects []runtime.Object
			if tt.template != nil {
				objects = append(objects, tt.template)
			}
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()

			builder := NewBuilder(tt.buildkit, client)
			pod, err := builder.BuildPod(t.Context())

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pod)

			// Marshal pod to YAML for golden file comparison
			yamlBytes, err := yaml.Marshal(pod)
			require.NoError(t, err)

			autogold.ExpectFile(t, autogold.Raw(yamlBytes))
		})
	}
}
