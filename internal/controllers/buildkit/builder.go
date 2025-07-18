// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit/resources"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit_template"
	"github.com/seatgeek/buildkit-operator/internal/merge"
)

type Builder struct {
	buildkit *v1alpha1.Buildkit
	cl       client.Reader
}

func NewBuilder(buildkit *v1alpha1.Buildkit, cl client.Reader) *Builder {
	return &Builder{
		buildkit: buildkit,
		cl:       cl,
	}
}

func (b *Builder) BuildPod(ctx context.Context) (*corev1.Pod, error) {
	// Load the referenced BuildkitTemplate
	var template v1alpha1.BuildkitTemplate
	key := client.ObjectKey{Name: b.buildkit.Spec.Template, Namespace: b.buildkit.Namespace}
	if err := b.cl.Get(ctx, key, &template); err != nil {
		return nil, err
	}

	// We define the overrideable defaults first; non-overrideable values will be set further down
	const buildkitContainerName = "buildkit"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: b.buildkit.Spec.Annotations,
			Labels: merge.Maps(
				map[string]string{"app.kubernetes.io/name": "buildkit"},
				b.buildkit.Spec.Labels,
			),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  buildkitContainerName,
					Image: "moby/buildkit:latest",
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "buildkitd",
							MountPath: "/home/user/.local/share/buildkit",
						},
					},
					Args: []string{
						"--addr",
						fmt.Sprintf("tcp://0.0.0.0:%d", template.Spec.Port),
					},
					Ports: []corev1.ContainerPort{
						{
							Name:          "tcp",
							ContainerPort: template.Spec.Port,
							Protocol:      "TCP",
						},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							GRPC: &corev1.GRPCAction{
								Port: template.Spec.Port,
							},
						},
						InitialDelaySeconds: 5,
						PeriodSeconds:       15,
						FailureThreshold:    2,
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							GRPC: &corev1.GRPCAction{
								Port: template.Spec.Port,
							},
						},
						InitialDelaySeconds: 5,
						PeriodSeconds:       30,
						FailureThreshold:    6,
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "buildkitd",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: ptr.To(int64(900)), // 15 minutes
		},
	}

	// Create a reference to the main container to keep the following code cleaner
	container := &pod.Spec.Containers[0]

	// Mount config map if needed
	if configMap := buildkit_template.NewBuilder(&template).ConfigMap(); configMap != nil {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMap.Name,
					},
				},
			},
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "config",
			MountPath: "/home/user/.config/buildkit",
		})
	}

	err := merge.Objects(
		// Start with the default, fully-overrideable configs first
		pod,
		// Then apply the pod template from the BuildkitTemplate
		corev1.Pod{
			ObjectMeta: template.Spec.PodTemplate.ObjectMeta,
			Spec:       template.Spec.PodTemplate.Spec,
		},
		// Then apply required metadata and resource overrides
		corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: b.buildkit.Name + "-",
				Name:         "",
				Namespace:    b.buildkit.Namespace,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:      buildkitContainerName,
						Resources: resources.Merge(b.buildkit.Spec.Resources, container.Resources),
					},
				},
			},
		},
	)

	return pod, err
}
