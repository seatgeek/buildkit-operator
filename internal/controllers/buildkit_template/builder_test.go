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
			name: "returns correct name for valid template",
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-template",
					Namespace: "test-namespace",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					BuildkitdToml: someToml,
				},
			},
			want: "buildkit-test-template-toml",
		},
		{
			name: "returns correct name even when TOML not given",
			template: &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-toml-template",
					Namespace: "test-namespace",
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					BuildkitdToml: "",
				},
			},
			want: "buildkit-empty-toml-template-toml",
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
