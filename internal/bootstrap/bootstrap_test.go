// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package bootstrap_test

import (
	"reflect"
	"testing"

	"github.com/iancoleman/strcase"
	"github.com/reddit/achilles-sdk/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/bootstrap"
	intscheme "github.com/seatgeek/buildkit-operator/internal/scheme"
)

// TestCacheOptions_SelectorsMatchFSMLabels asserts that the cache label
// selectors match the labels the achilles-sdk FSM stamps on applied objects:
// meta.ManagedByKey set to the kebab-cased, scheme-registered Kind of the
// reconciled resource. If either controller's reconciled type is renamed, or
// the cache filter drifts from the FSM's labeling, managed objects would
// become invisible to the operator and it would spawn duplicates.
func TestCacheOptions_SelectorsMatchFSMLabels(t *testing.T) {
	t.Parallel()

	scheme := intscheme.MustNewScheme()

	tests := []struct {
		name           string
		cachedObject   client.Object
		reconciledType client.Object
	}{
		{
			name:           "Pods are cached for the Buildkit controller",
			cachedObject:   &corev1.Pod{},
			reconciledType: &v1alpha1.Buildkit{},
		},
		{
			name:           "ConfigMaps are cached for the BuildkitTemplate controller",
			cachedObject:   &corev1.ConfigMap{},
			reconciledType: &v1alpha1.BuildkitTemplate{},
		},
	}

	opts := bootstrap.CacheOptions(nil)
	require.Len(t, opts.ByObject, len(tests), "every ByObject entry must be covered by a test case")

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// ByObject is keyed by example objects; look up by type since the
			// map keys are pointers
			var byObject *cache.ByObject
			for k, v := range opts.ByObject {
				if reflect.TypeOf(k) == reflect.TypeOf(tc.cachedObject) {
					byObject = &v
					break
				}
			}
			require.NotNil(t, byObject, "expected a ByObject entry for %T", tc.cachedObject)
			require.NotNil(t, byObject.Label)

			// derive the stamped labels exactly as the achilles-sdk FSM does:
			// controller name = strcase.ToKebab of the scheme-registered Kind
			gvk, err := apiutil.GVKForObject(tc.reconciledType, scheme)
			require.NoError(t, err)
			stamped := meta.RedditLabels(strcase.ToKebab(gvk.Kind))

			assert.True(t, byObject.Label.Matches(labels.Set(stamped)),
				"selector %q must match FSM-stamped labels %v", byObject.Label, stamped)
			assert.False(t, byObject.Label.Matches(labels.Set{}),
				"selector must not match unlabeled objects")
		})
	}
}
