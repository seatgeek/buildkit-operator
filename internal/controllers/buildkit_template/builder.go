// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_template

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/prestop"
)

type Builder struct {
	template *v1alpha1.BuildkitTemplate
}

func NewBuilder(template *v1alpha1.BuildkitTemplate) Builder {
	return Builder{
		template: template,
	}
}

func (b Builder) AllConfigMaps() map[string]*corev1.ConfigMap {
	return map[string]*corev1.ConfigMap{
		b.configMapName():        b.ConfigMap(),
		b.scriptsConfigMapName(): b.ScriptsConfigMap(),
	}
}

// configMapName returns the potential name of the ConfigMap that _might_ be created for the BuildkitTemplate.
func (b Builder) configMapName() string {
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
			Name:      b.configMapName(),
			Namespace: b.template.Namespace,
		},
		Data: map[string]string{
			"buildkitd.toml": b.template.Spec.BuildkitdToml,
		},
	}
}

func (b Builder) scriptsConfigMapName() string {
	if b.template == nil {
		return ""
	}
	return fmt.Sprintf("buildkit-%s-scripts", b.template.Name)
}

const PreStopScriptName = "buildkit-prestop.sh"

func (b Builder) ScriptsConfigMap() *corev1.ConfigMap {
	if b.template == nil || !b.template.Spec.Lifecycle.PreStopScript {
		return nil
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("buildkit-%s-scripts", b.template.Name),
			Namespace: b.template.Namespace,
		},
		Data: map[string]string{
			PreStopScriptName: prestop.Script(b.template.Spec.Port),
		},
	}
}
