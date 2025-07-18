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
	// PodTemplate is the Pod template used to create the Buildkit instances
	// +kubebuilder:validation:Required
	PodTemplate corev1.PodTemplateSpec `json:"template"`

	// BuildkitdToml is the configuration for Buildkit in TOML format
	// +kubebuilder:validation:Required
	BuildkitdToml string `json:"buildkitdToml"`

	// Port is the TCP port number on which the Buildkit instance will listen; default is 1234
	Port int32 `json:"port"`

	// RequireOwner indicates whether the Buildkit instance must be created with an owner reference
	RequireOwner bool `json:"requireOwner,omitempty"`
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
