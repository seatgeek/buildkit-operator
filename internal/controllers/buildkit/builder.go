// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit/resources"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit_template"
	"github.com/seatgeek/buildkit-operator/internal/merge"
)

// SecretClientCAKey is the TLS Secret key holding the Buildkit's client CA.
const SecretClientCAKey = "ca.crt"

// tlsCACertFlag makes buildkitd require client certificates signed by the given CA. Its presence in a
// pod's args is how the controller tells a TLS pod from a plaintext one.
const tlsCACertFlag = "--tlscacert"

const (
	tlsVolumeName    = "buildkitd-tls"
	tlsMountPath     = "/etc/buildkit-tls"
	socketVolumeName = "buildkitd-socket"
)

// TLSOptions enables mutual TLS on the buildkitd TCP listener.
type TLSOptions struct {
	// SecretName names a Secret in the Buildkit's namespace holding tls.crt, tls.key (the server
	// certificate) and ca.crt (the client CA).
	SecretName string
	// HealthProxyImage runs the health-only sidecar that kubelet gRPC probes talk to.
	HealthProxyImage string
	// ServerCertSHA256 fingerprints the server certificate in the Secret, recorded on the pod as
	// ServerCertAnnotation so the controller can tell which certificate a pod was started with.
	ServerCertSHA256 string
}

// ServerCertAnnotation records the fingerprint of the server certificate a TLS pod was started with.
const ServerCertAnnotation = "buildkit.seatgeek.io/server-cert-sha256"

type Builder struct {
	buildkit *v1alpha1.Buildkit
	cl       client.Reader
	tls      *TLSOptions
}

func NewBuilder(buildkit *v1alpha1.Buildkit, cl client.Reader) *Builder {
	return &Builder{
		buildkit: buildkit,
		cl:       cl,
	}
}

// WithTLS configures the pod for mutual TLS. It is required when the template sets requireClientTLS.
func (b *Builder) WithTLS(opts *TLSOptions) *Builder {
	b.tls = opts
	return b
}

func (b *Builder) BuildPod(ctx context.Context) (*corev1.Pod, error) {
	// Load the referenced BuildkitTemplate
	var template v1alpha1.BuildkitTemplate
	key := client.ObjectKey{Name: b.buildkit.Spec.Template, Namespace: b.buildkit.Namespace}
	if err := b.cl.Get(ctx, key, &template); err != nil {
		return nil, err
	}

	// Never fall back to a plaintext listener for a template that requires TLS.
	if template.Spec.RequireClientTLS && b.tls == nil {
		return nil, fmt.Errorf("BuildkitTemplate %q requires client TLS but no TLS configuration was provided", template.Name)
	}

	// We define the overrideable defaults first; non-overrideable values will be set further down
	const buildkitContainerName = "buildkit"
	pod := &corev1.Pod{
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
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            buildkitContainerName,
					Image:           template.Spec.Image,
					ImagePullPolicy: template.Spec.ImagePullPolicy,
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
					Resources: resources.WithMaximums(template.Spec.Resources.Maximum, template.Spec.Resources.Default, b.buildkit.Spec.Resources),
					StartupProbe: &corev1.Probe{
						GRPC: &corev1.GRPCAction{
							Port: template.Spec.Port,
						},
						PeriodSeconds:    2,
						FailureThreshold: 15,
					},
					ReadinessProbe: &corev1.Probe{
						GRPC: &corev1.GRPCAction{
							Port: template.Spec.Port,
						},
						PeriodSeconds:    15,
						FailureThreshold: 2,
					},
					LivenessProbe: &corev1.Probe{
						GRPC: &corev1.GRPCAction{
							Port: template.Spec.Port,
						},
						TimeoutSeconds:   3,
						PeriodSeconds:    30,
						FailureThreshold: 6,
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: new(true),
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name:     "buildkitd",
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
			HostUsers:                     template.Spec.HostUsers,
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
			RunAsUser:  new(int64(1000)),
			RunAsGroup: new(int64(1000)),
		}
	}

	if b.tls != nil {
		if err := b.configureTLS(pod, &template); err != nil {
			return nil, err
		}
	}

	if template.Spec.Observability.DebugLogging {
		container.Args = append(container.Args, "--debug")
	}

	if template.Spec.Observability.OTLP != nil {
		var attrs strings.Builder
		attrs.WriteString("service.name=")
		attrs.WriteString(template.Spec.Observability.OTLP.ServiceName)

		for k, v := range template.Spec.Observability.OTLP.ResourceAttributes {
			attrs.WriteRune(',')
			attrs.WriteString(k)
			attrs.WriteRune('=')
			attrs.WriteString(v)
		}

		container.Env = append(container.Env,
			corev1.EnvVar{
				Name: "HOST_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "status.hostIP",
					},
				},
			},
			corev1.EnvVar{
				Name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
				Value: "http://$(HOST_IP):4317",
			},
			corev1.EnvVar{
				Name:  "OTEL_SERVICE_NAME",
				Value: template.Spec.Observability.OTLP.ServiceName,
			},
			corev1.EnvVar{
				Name:  "OTEL_RESOURCE_ATTRIBUTES",
				Value: attrs.String(),
			},
		)
	}

	// Mount buildkitd.toml config map if needed
	if configMap := buildkit_template.NewBuilder(&template).ConfigMap(); configMap != nil {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "config",
			ConfigMap: &corev1.ConfigMapVolumeSource{
				Name: configMap.Name,
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
			ConfigMap: &corev1.ConfigMapVolumeSource{
				Name:        configMap.Name,
				DefaultMode: new(int32(0o755)),
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

// configureTLS makes buildkitd's TCP listener require client certificates signed by the Buildkit's
// client CA, and moves kubelet's gRPC probes to a health-only sidecar that reaches buildkitd over its
// unix socket. It must run after the rootless adjustments, which change the socket path.
func (b *Builder) configureTLS(pod *corev1.Pod, template *v1alpha1.BuildkitTemplate) error {
	if b.tls.SecretName == "" || b.tls.HealthProxyImage == "" || b.tls.ServerCertSHA256 == "" {
		return errors.New("TLS requires a secret name, a health proxy image and a server certificate fingerprint")
	}

	container := &pod.Spec.Containers[0]

	socketPath, ok := strings.CutPrefix(container.Args[1], "unix://")
	if !ok {
		return fmt.Errorf("unexpected buildkitd socket address %q", container.Args[1])
	}
	socketDir := path.Dir(socketPath)

	// The socket is owned by buildkitd's group with mode 0660, so the sidecar joins that group
	// instead of running as buildkitd's user.
	socketGID := int64(0)
	if template.Spec.Rootless {
		socketGID = 1000
	}

	// Set last so annotations from the Buildkit or template can't override it.
	pod.Annotations = merge.Maps(pod.Annotations, map[string]string{ServerCertAnnotation: b.tls.ServerCertSHA256})

	pod.Spec.Volumes = append(pod.Spec.Volumes,
		corev1.Volume{
			Name: tlsVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: b.tls.SecretName,
				// buildkitd may run as a non-root user (1000 when rootless) that does not own the
				// projected files. Only the buildkit container mounts this volume.
				DefaultMode: new(int32(0o444)),
			},
		},
		corev1.Volume{
			Name:     socketVolumeName,
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	)

	container.VolumeMounts = append(container.VolumeMounts,
		corev1.VolumeMount{Name: tlsVolumeName, MountPath: tlsMountPath, ReadOnly: true},
		corev1.VolumeMount{Name: socketVolumeName, MountPath: socketDir},
	)

	container.Args = append(container.Args,
		"--tlscert", path.Join(tlsMountPath, corev1.TLSCertKey),
		"--tlskey", path.Join(tlsMountPath, corev1.TLSPrivateKeyKey),
		tlsCACertFlag, path.Join(tlsMountPath, SecretClientCAKey),
	)

	for _, probe := range []*corev1.Probe{container.StartupProbe, container.ReadinessProbe, container.LivenessProbe} {
		probe.GRPC.Port = template.Spec.HealthPort
	}

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:            "health-proxy",
		Image:           b.tls.HealthProxyImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{
			"/operator", "health-proxy",
			"--listen", fmt.Sprintf(":%d", template.Spec.HealthPort),
			"--socket", socketPath,
		},
		// Native sidecar: starts before buildkit and runs for the pod's lifetime.
		RestartPolicy: new(corev1.ContainerRestartPolicyAlways),
		Ports: []corev1.ContainerPort{
			{Name: "health", ContainerPort: template.Spec.HealthPort, Protocol: corev1.ProtocolTCP},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: socketVolumeName, MountPath: socketDir},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("16Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                new(int64(65532)),
			RunAsGroup:               new(socketGID),
			RunAsNonRoot:             new(true),
			ReadOnlyRootFilesystem:   new(true),
			AllowPrivilegeEscalation: new(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	})

	return nil
}
