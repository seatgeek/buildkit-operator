// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_template

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

type Builder struct {
	template *v1alpha1.BuildkitTemplate
}

func NewBuilder(template *v1alpha1.BuildkitTemplate) Builder {
	return Builder{
		template: template,
	}
}

// ConfigMapName returns the potential name of the ConfigMap that _might_ be created for the BuildkitTemplate.
func (b Builder) ConfigMapName() string {
	if b.template == nil {
		return ""
	}
	return fmt.Sprintf("buildkit-%s-toml", b.template.Name)
}

// ConfigMap returns a ConfigMap containing the buildkitd.toml configuration from the BuildkitTemplate.
// If the BuildkitTemplate does not have a buildkitd.toml, it returns nil.
func (b Builder) ConfigMap() *corev1.ConfigMap {
	if b.template == nil || b.template.Spec.BuildkitdToml == "" {
		return nil
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.ConfigMapName(),
			Namespace: b.template.Namespace,
		},
		Data: map[string]string{
			"buildkitd.toml": b.template.Spec.BuildkitdToml,
		},
	}
}
