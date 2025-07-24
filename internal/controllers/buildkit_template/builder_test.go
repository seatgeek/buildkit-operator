// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/prestop"
)

const someToml = `[worker.oci]
enabled = true
max-parallelism = 4

[worker.containerd]
enabled = false

[registry."docker.io"]
mirrors = ["mirror.gcr.io"]`

func TestBuilder_ConfigMapName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template *v1alpha1.BuildkitTemplate
		want     string
	}{
		{
			name: "returns correct name for template",
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-namespace",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{},
			},
			want: "buildkit-test-template-toml",
		},
		{
			name:     "returns empty string when template is nil",
			template: nil,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewBuilder(tt.template)
			got := builder.ConfigMapName()

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuilder_ConfigMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template *v1alpha1.BuildkitTemplate
		want     *corev1.ConfigMap
	}{
		{
			name: "returns configmap when BuildkitdToml is not empty",
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-namespace",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					BuildkitdToml: someToml,
				},
			},
			want: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "buildkit-test-template-toml",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"buildkitd.toml": someToml,
				},
			},
		},
		{
			name: "returns nil when BuildkitdToml is empty",
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-namespace",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					BuildkitdToml: "",
				},
			},
			want: nil,
		},
		{
			name:     "returns nil when template is nil",
			template: nil,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewBuilder(tt.template)
			got := builder.ConfigMap()

			if tt.want == nil {
				assert.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			assert.Equal(t, tt.want.Name, got.Name)
			assert.Equal(t, tt.want.Namespace, got.Namespace)
			assert.Equal(t, tt.want.Data, got.Data)
		})
	}
}

func TestBuilder_ScriptsConfigMapName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template *v1alpha1.BuildkitTemplate
		want     string
	}{
		{
			name: "returns correct name for template",
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-namespace",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{},
			},
			want: "buildkit-test-template-scripts",
		},
		{
			name:     "returns empty string when template is nil",
			template: nil,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewBuilder(tt.template)
			got := builder.ScriptsConfigMapName()

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuilder_ScriptsConfigMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template *v1alpha1.BuildkitTemplate
		want     *corev1.ConfigMap
	}{
		{
			name: "returns configmap when pre-stop script is needed",
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-namespace",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					Port: 1234,
					Lifecycle: v1alpha1.BuildkitTemplatePodLifecycle{
						PreStopScript: true,
					},
				},
			},
			want: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "buildkit-test-template-scripts",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"buildkit-prestop.sh": prestop.Script(1234),
				},
			},
		},
		{
			name: "returns nil when pre-stop script is not needed",
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-namespace",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{},
			},
			want: nil,
		},
		{
			name:     "returns nil when template is nil",
			template: nil,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewBuilder(tt.template)
			got := builder.ScriptsConfigMap()

			if tt.want == nil {
				assert.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			assert.Equal(t, tt.want.Name, got.Name)
			assert.Equal(t, tt.want.Namespace, got.Namespace)
			assert.Equal(t, tt.want.Data, got.Data)
		})
	}
}
