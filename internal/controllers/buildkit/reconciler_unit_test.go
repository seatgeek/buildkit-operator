// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
