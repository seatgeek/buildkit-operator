// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package v1alpha1

import (
	"github.com/reddit/achilles-sdk-api/api"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	TypeDeployed api.ConditionType = "Deployed"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=buildkit
// +kubebuilder:subresource:status
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.template`
type Buildkit struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BuildkitSpec   `json:"spec,omitempty"`
	Status BuildkitStatus `json:"status,omitempty"`
}

type BuildkitSpec struct {
	// Template is the name of the BuildkitTemplate to use for creating the Buildkit instance.
	// +kubebuilder:validation:Required
	Template string `json:"template"`

	// Resources defines the resource requirements for the Buildkit instance.
	// It is optional and can be omitted if the default resource limits are sufficient.
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Annotations can be used to attach arbitrary metadata to the Buildkit instance.
	Annotations map[string]string `json:"annotations,omitempty"`
	// Labels can be used to attach arbitrary metadata to the Buildkit instance.
	Labels map[string]string `json:"labels,omitempty"`
}

type BuildkitStatus struct {
	api.ConditionedStatus `json:",inline"`

	// ResourceRefs is a list of all resources managed by this object.
	ResourceRefs []api.TypedObjectRef `json:"resourceRefs,omitempty"`

	// Endpoint is the tcp URI of the Buildkit instance, like tcp://some-buildkit-instance-amd64:1234
	Endpoint string `json:"endpoint,omitempty"`
}

func (b *Buildkit) GetConditions() []api.Condition {
	return b.Status.Conditions
}

func (b *Buildkit) SetConditions(cond ...api.Condition) {
	b.Status.SetConditions(cond...)
}

func (b *Buildkit) GetCondition(t api.ConditionType) api.Condition {
	return b.Status.GetCondition(t)
}

func (b *Buildkit) SetManagedResources(refs []api.TypedObjectRef) {
	b.Status.ResourceRefs = refs
}

func (b *Buildkit) GetManagedResources() []api.TypedObjectRef {
	return b.Status.ResourceRefs
}

// +kubebuilder:object:root=true
type BuildkitList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Buildkit `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Buildkit{}, &BuildkitList{})
}
