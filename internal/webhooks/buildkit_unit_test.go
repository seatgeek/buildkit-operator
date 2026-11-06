// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package webhooks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/test"
)

func TestBuildkitValidator_warnsWhenClientCAExpiresBeforeDeadline(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	deadline := int64(2 * 60 * 60)
	template := &v1alpha1.BuildkitTemplate{
		Name:      "tls",
		Namespace: "ns",
		Spec: v1alpha1.BuildkitTemplateSpec{
			RequireClientTLS: true,
			Lifecycle:        v1alpha1.BuildkitTemplatePodLifecycle{ActiveDeadlineSeconds: &deadline},
		},
	}
	v := NewBuildkitValidator(fake.NewClientBuilder().WithScheme(scheme).WithObjects(template).Build())

	buildkit := func(caValidFor time.Duration) *v1alpha1.Buildkit {
		return &v1alpha1.Buildkit{
			Name:      "bk",
			Namespace: "ns",
			Spec: v1alpha1.BuildkitSpec{
				Template: template.Name,
				ClientCA: test.CACertPEMValid(time.Now().Add(-time.Minute), time.Now().Add(caValidFor)),
			},
		}
	}

	warnings, err := v.ValidateCreate(t.Context(), buildkit(time.Hour))
	require.NoError(t, err)
	assert.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "before this instance's pod deadline")

	warnings, err = v.ValidateCreate(t.Context(), buildkit(3*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, warnings)
}
