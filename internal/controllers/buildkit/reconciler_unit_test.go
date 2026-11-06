// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/test"
)

func TestNotYetDeleting(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	pod := func(name string, deleting bool, grace *int64) corev1.Pod {
		p := corev1.Pod{Name: name, DeletionGracePeriodSeconds: grace}
		if deleting {
			p.DeletionTimestamp = &now
		}
		return p
	}

	pods := []corev1.Pod{
		pod("running", false, nil),
		pod("draining-long", true, new(int64(900))),
		pod("draining-unknown", true, nil),
		pod("force-deleted", true, new(int64(1))),
		pod("gone-now", true, new(int64(0))),
	}

	got := notYetDeleting(pods)
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"running", "draining-long", "draining-unknown"}, names,
		"a pod draining on a long grace period still needs its deletion shortened")
}

func TestRecheckAtClientCAExpiry(t *testing.T) {
	t.Parallel()

	now := time.Now().Truncate(time.Second)
	expiry := now.Add(36 * time.Hour)
	r := &reconciler{now: func() time.Time { return now }}
	obj := &v1alpha1.Buildkit{Spec: v1alpha1.BuildkitSpec{ClientCA: test.CACertPEMValid(now.Add(-time.Minute), expiry)}}

	result := r.recheckAtClientCAExpiry(obj)
	require.NoError(t, result.Err)
	assert.True(t, result.Done, "the instance stays ready until the CA expires")
	assert.False(t, result.RequeueAfterCompletion, "requeueing after completion would mark the instance not ready")
	assert.Nil(t, result.CustomStatusCondition)
	assert.Equal(t, 36*time.Hour+time.Second, result.RequeueAfter, "must reconcile once the CA has expired, not at the instant it still counts as valid")
	assert.NotEmpty(t, clientCAProblem(obj.Spec.ClientCA, now.Add(result.RequeueAfter)), "the requeued reconcile must see the CA as expired")
}
