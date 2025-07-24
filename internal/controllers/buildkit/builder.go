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
			GenerateName: b.buildkit.Name + "-",
			Name:         "",
			Namespace:    b.buildkit.Namespace,
			Annotations: merge.Maps(
				b.buildkit.Spec.Annotations,
				template.Spec.PodAnnotations,
			),
			Labels: merge.Maps(
				map[string]string{"app.kubernetes.io/name": "buildkit"},
				b.buildkit.Spec.Labels,
				template.Spec.PodLabels,
			),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  buildkitContainerName,
					Image: template.Spec.Image,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "buildkitd",
							MountPath: "/var/lib/buildkit",
						},
					},
					Command: template.Spec.Command,
					Args: []string{
						"--addr",
						"unix:///run/buildkit/buildkitd.sock",
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
					Resources: resources.Merge(b.buildkit.Spec.Resources, template.Spec.Resources),
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
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr.To(true),
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
			ServiceAccountName:            template.Spec.ServiceAccountName,
			NodeSelector:                  template.Spec.Scheduling.NodeSelector,
			Tolerations:                   template.Spec.Scheduling.Tolerations,
			Affinity:                      template.Spec.Scheduling.Affinity,
			TopologySpreadConstraints:     template.Spec.Scheduling.TopologySpreadConstraints,
			PriorityClassName:             template.Spec.Scheduling.PriorityClassName,
			RestartPolicy:                 template.Spec.Lifecycle.RestartPolicy,
			TerminationGracePeriodSeconds: template.Spec.Lifecycle.TerminationGracePeriodSeconds,
			ActiveDeadlineSeconds:         template.Spec.Lifecycle.ActiveDeadlineSeconds,
		},
	}

	// Create a reference to the main container to keep the following code cleaner
	container := &pod.Spec.Containers[0]

	if template.Spec.Rootless {
		pod.Annotations = merge.Maps(pod.Annotations, map[string]string{
			"container.apparmor.security.beta.kubernetes.io/" + buildkitContainerName: "unconfined",
		})
		container.VolumeMounts[0].MountPath = "/home/user/.local/share/buildkit"
		container.Args[1] = "unix:///run/user/1000/buildkit/buildkitd.sock"
		container.Args = append(container.Args, "--oci-worker-no-process-sandbox")
		container.SecurityContext = &corev1.SecurityContext{
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeUnconfined,
			},
			RunAsUser:  ptr.To(int64(1000)),
			RunAsGroup: ptr.To(int64(1000)),
		}
	}

	if template.Spec.DebugLogging {
		container.Args = append(container.Args, "--debug")
	}

	// Mount buildkitd.toml config map if needed
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

		mountPath := "/etc/buildkit"
		if template.Spec.Rootless {
			mountPath = "/home/user/.config/buildkit"
		}

		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "config",
			MountPath: mountPath,
		})
	}

	// Configure pre-stop script if needed
	if configMap := buildkit_template.NewBuilder(&template).ScriptsConfigMap(); configMap != nil {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "scripts",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMap.Name,
					},
					DefaultMode: ptr.To(int32(0o755)),
				},
			},
		})

		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "scripts",
			MountPath: "/usr/local/bin/buildkit-prestop.sh",
			SubPath:   buildkit_template.PreStopScriptName,
		})

		container.Lifecycle = &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/sh", "/usr/local/bin/buildkit-prestop.sh"},
				},
			},
		}
	}

	return pod, nil
}
