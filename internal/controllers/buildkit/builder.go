// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit/resources"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit_template"
	"github.com/seatgeek/buildkit-operator/internal/merge"
	"github.com/seatgeek/buildkit-operator/internal/tlsutil"
)

// tlsCertMargin is how much longer than its pod a server certificate stays valid.
const tlsCertMargin = 30 * time.Minute

// tlsPodDeadline returns the activeDeadlineSeconds a TLS pod runs with.
func tlsPodDeadline(template *v1alpha1.BuildkitTemplate) int64 {
	if d := template.Spec.Lifecycle.ActiveDeadlineSeconds; d != nil {
		return *d
	}
	return v1alpha1.DefaultTLSActiveDeadlineSeconds
}

// tlsCertValidity is how long a TLS pod's server certificate is valid from when the pod mints it.
// Minting happens after the pod starts, and the deadline counts from the start, so the certificate
// outlives the pod's deadline and termination grace period by at least tlsCertMargin.
func tlsCertValidity(template *v1alpha1.BuildkitTemplate) time.Duration {
	grace := int64(corev1.DefaultTerminationGracePeriodSeconds)
	if g := template.Spec.Lifecycle.TerminationGracePeriodSeconds; g != nil {
		grace = *g
	}
	return time.Duration(tlsPodDeadline(template)+grace)*time.Second + tlsCertMargin
}

// tlsCACertFlag makes buildkitd require client certificates signed by the given CA. Its presence in a
// pod's args is how the controller tells a TLS pod from a plaintext one.
const tlsCACertFlag = "--tlscacert"

const (
	tlsVolumeName    = "buildkitd-tls"
	tlsMountPath     = "/etc/buildkit-tls"
	socketVolumeName = "buildkitd-socket"
)

type Builder struct {
	buildkit *v1alpha1.Buildkit
	cl       client.Reader
	// tls, when its image is set, enables mutual TLS.
	tls TLSHelpers
}

func NewBuilder(buildkit *v1alpha1.Buildkit, cl client.Reader) *Builder {
	return &Builder{
		buildkit: buildkit,
		cl:       cl,
	}
}

// TLSHelpers is the image the gen-cert and health-proxy containers of TLS pods run, and how it is
// pulled. Both come from the operator's configuration, not the template.
type TLSHelpers struct {
	Image string
	// PullPolicy is empty for Kubernetes' default.
	PullPolicy corev1.PullPolicy
}

// WithTLS configures the pod for mutual TLS. It is required when the template sets requireClientTLS.
func (b *Builder) WithTLS(helpers TLSHelpers) *Builder {
	b.tls = helpers
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
	if template.Spec.RequireClientTLS && b.tls.Image == "" {
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

	if b.tls.Image != "" {
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

// genCertContainerName is the init container that mints the pod's server certificate and reports it
// in its termination message.
const genCertContainerName = "gen-cert"

// configureTLS makes buildkitd's TCP listener require client certificates signed by the Buildkit's
// client CA, using a server certificate the pod mints for itself, and moves kubelet's gRPC probes to a
// health-only sidecar that reaches buildkitd over its unix socket. It must run after the rootless
// adjustments, which change the socket path.
func (b *Builder) configureTLS(pod *corev1.Pod, template *v1alpha1.BuildkitTemplate) error {
	container := &pod.Spec.Containers[0]

	socketPath, ok := strings.CutPrefix(container.Args[1], "unix://")
	if !ok {
		return fmt.Errorf("unexpected buildkitd socket address %q", container.Args[1])
	}
	socketDir := path.Dir(socketPath)

	// buildkitd's group owns its socket (mode 0660), so the sidecar joins that group instead of running
	// as buildkitd's user. Rootless buildkitd runs as 1000 and must own its key file; rootful buildkitd
	// is root and can read any file.
	helperUID, helperGID := int64(65532), int64(0)
	if template.Spec.Rootless {
		helperUID, helperGID = 1000, 1000
	}

	pod.Spec.Volumes = append(pod.Spec.Volumes,
		corev1.Volume{
			Name: tlsVolumeName,
			// The private key lives only in memory, for the pod's lifetime.
			EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
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

	// The server certificate is minted to outlive the pod, so the pod must have a deadline.
	pod.Spec.ActiveDeadlineSeconds = new(tlsPodDeadline(template))

	container.Args = append(container.Args,
		"--tlscert", path.Join(tlsMountPath, tlsutil.CertFile),
		"--tlskey", path.Join(tlsMountPath, tlsutil.KeyFile),
		tlsCACertFlag, path.Join(tlsMountPath, tlsutil.ClientCAFile),
	)

	for _, probe := range []*corev1.Probe{container.StartupProbe, container.ReadinessProbe, container.LivenessProbe} {
		probe.GRPC.Port = template.Spec.HealthPort
	}

	genCert := b.tlsHelperContainer(genCertContainerName, helperUID, helperGID)
	genCert.Command = []string{
		"/operator", "gen-cert",
		"--dir", tlsMountPath,
		"--server-name", b.buildkit.Name,
		"--valid-for", tlsCertValidity(template).String(),
	}
	genCert.Env = []corev1.EnvVar{{Name: "CLIENT_CA", Value: b.buildkit.Spec.ClientCA}}
	// The controller reads the certificate from this message; never let container logs stand in for it.
	genCert.TerminationMessagePath = corev1.TerminationMessagePathDefault
	genCert.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	genCert.VolumeMounts = []corev1.VolumeMount{{Name: tlsVolumeName, MountPath: tlsMountPath}}

	healthProxy := b.tlsHelperContainer("health-proxy", 65532, helperGID)
	healthProxy.Command = []string{
		"/operator", "health-proxy",
		"--listen", fmt.Sprintf(":%d", template.Spec.HealthPort),
		"--socket", socketPath,
	}
	// Native sidecar: starts after gen-cert, before buildkit, and runs for the pod's lifetime.
	healthProxy.RestartPolicy = new(corev1.ContainerRestartPolicyAlways)
	healthProxy.Ports = []corev1.ContainerPort{
		{Name: "health", ContainerPort: template.Spec.HealthPort, Protocol: corev1.ProtocolTCP},
	}
	healthProxy.VolumeMounts = []corev1.VolumeMount{{Name: socketVolumeName, MountPath: socketDir}}

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, genCert, healthProxy)

	return nil
}

// tlsHelperContainer returns the hardened base of the small operator-image containers in a TLS pod.
func (b *Builder) tlsHelperContainer(name string, uid, gid int64) corev1.Container {
	return corev1.Container{
		Name:            name,
		Image:           b.tls.Image,
		ImagePullPolicy: b.tls.PullPolicy,
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
			RunAsUser:                new(uid),
			RunAsGroup:               new(gid),
			RunAsNonRoot:             new(true),
			ReadOnlyRootFilesystem:   new(true),
			AllowPrivilegeEscalation: new(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
}
