// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package v1alpha1

import (
	"github.com/reddit/achilles-sdk-api/api"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const BuildkitTemplateNameMaxLength = 57

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=buildkittemplate
// +kubebuilder:subresource:status
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type BuildkitTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BuildkitTemplateSpec   `json:"spec,omitempty"`
	Status BuildkitTemplateStatus `json:"status,omitempty"`
}

type BuildkitTemplateSpec struct {
	// +kubebuilder:validation:Optional
	PodLabels map[string]string `json:"podLabels,omitempty"`
	// +kubebuilder:validation:Optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// +kubebuilder:validation:Optional
	Rootless bool `json:"rootless,omitempty"`

	// Port is the TCP port number on which the Buildkit instance will listen; default is 1234
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1234
	Port int32 `json:"port"`

	// BuildkitdToml is the configuration for Buildkit in TOML format
	// +kubebuilder:validation:Optional
	BuildkitdToml string `json:"buildkitdToml,omitempty"`

	// Image is the container image to use for the Buildkit instance
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="moby/buildkit:latest"
	Image string `json:"image,omitempty"`

	// ImagePullPolicy defines the image pull policy for the Buildkit instance
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=IfNotPresent
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	Resources BuildkitTemplateResources `json:"resources,omitempty"`

	// +kubebuilder:validation:Optional
	Command []string `json:"command,omitempty"`

	// +kubebuilder:validation:Optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Scheduling defines the scheduling constraints for the Buildkit pods
	// +kubebuilder:validation:Optional
	Scheduling BuildkitTemplatePodScheduling `json:"scheduling,omitempty"`

	// Lifecycle defines the lifecycle settings for the Buildkit pods
	// +kubebuilder:validation:Optional
	Lifecycle BuildkitTemplatePodLifecycle `json:"lifecycle,omitempty"`

	// Observability defines the observability settings for the Buildkit pods
	// +kubebuilder:validation:Optional
	Observability BuildkitTemplateObservability `json:"observability,omitempty"`

	// HostUsers defines if the host's user namespace should be used
	// If set to true or not present, the pod will be run in the host user namespace, useful
	// for when the pod needs a feature only available to the host user namespace, such as
	// loading a kernel module with CAP_SYS_MODULE.
	// When set to false, a new userns is created for the pod. Setting false is useful for
	// mitigating container breakout vulnerabilities even allowing users to run their
	// containers as root without actually having root privileges on the host.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	HostUsers *bool `json:"hostUsers,omitempty"`
}

type BuildkitTemplatePodScheduling struct {
	// +kubebuilder:validation:Optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// +kubebuilder:validation:Optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// +kubebuilder:validation:Optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// +kubebuilder:validation:Optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

type BuildkitTemplatePodLifecycle struct {
	// RequireOwner indicates whether the Buildkit instance must be created with an owner reference
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	RequireOwner bool `json:"requireOwner,omitempty"`

	// +kubebuilder:validation:Optional
	RestartPolicy corev1.RestartPolicy `json:"restartPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=900
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// +kubebuilder:validation:Optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// +kubebuilder:validation:Optional
	PreStopScript bool `json:"preStopScript,omitempty"`
}

type BuildkitTemplateObservability struct {
	// +kubebuilder:validation:Optional
	DebugLogging bool `json:"debugLogging,omitempty"`

	// +kubebuilder:validation:Optional
	OTLP *BuildkitTemplateOTLPSettings `json:"otlp,omitempty"`
}

type BuildkitTemplateOTLPSettings struct {
	// +kubebuilder:validation:Required
	ServiceName string `json:"serviceName"`

	// +kubebuilder:validation:Optional
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty"`
}

type BuildkitTemplateResources struct {
	// +kubebuilder:validation:Optional
	Default corev1.ResourceRequirements `json:"default,omitempty"`

	// +kubebuilder:validation:Optional
	Maximum corev1.ResourceList `json:"maximum,omitempty"`
}

type BuildkitTemplateStatus struct {
	api.ConditionedStatus `json:",inline"`

	// ResourceRefs is a list of all resources managed by this object.
	ResourceRefs []api.TypedObjectRef `json:"resourceRefs,omitempty"`
}

func (b *BuildkitTemplate) GetConditions() []api.Condition {
	return b.Status.Conditions
}

func (b *BuildkitTemplate) SetConditions(cond ...api.Condition) {
	b.Status.SetConditions(cond...)
}

func (b *BuildkitTemplate) GetCondition(t api.ConditionType) api.Condition {
	return b.Status.GetCondition(t)
}

func (b *BuildkitTemplate) SetManagedResources(refs []api.TypedObjectRef) {
	b.Status.ResourceRefs = refs
}

func (b *BuildkitTemplate) GetManagedResources() []api.TypedObjectRef {
	return b.Status.ResourceRefs
}

// +kubebuilder:object:root=true
type BuildkitTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BuildkitTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BuildkitTemplate{}, &BuildkitTemplateList{})
}
