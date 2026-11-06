// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"testing"
	"time"

	"github.com/reddit/achilles-sdk-api/api"
	"github.com/reddit/achilles-sdk/pkg/fsm/types"
	"github.com/reddit/achilles-sdk/pkg/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/test"
	"github.com/seatgeek/buildkit-operator/internal/tlsutil"
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

func TestRunBuildkit_withoutHelperImage(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	template := &v1alpha1.BuildkitTemplate{Name: "t", Namespace: "ns", Spec: v1alpha1.BuildkitTemplateSpec{RequireClientTLS: true}}
	plaintext := &corev1.Pod{Name: "plaintext", Namespace: "ns", Spec: corev1.PodSpec{Containers: []corev1.Container{{Args: []string{"--addr", "tcp://0.0.0.0:1234"}}}}}
	obj := &v1alpha1.Buildkit{
		Name:      "bk",
		Namespace: "ns",
		Spec:      v1alpha1.BuildkitSpec{Template: "t", ClientCA: test.CACertPEM()},
		Status: v1alpha1.BuildkitStatus{
			Endpoint:     "tcp://10.0.0.1:1234",
			ResourceRefs: []api.TypedObjectRef{{Version: "v1", Kind: "Pod", Name: plaintext.Name, Namespace: plaintext.Namespace}},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(template, obj, plaintext).WithStatusSubresource(obj).Build()
	r := &reconciler{c: &io.ClientApplicator{Client: cl}, log: zap.NewNop().Sugar(), now: time.Now}

	_, result := r.runBuildkit().Transition(t.Context(), obj, types.NewOutputSet(scheme))
	require.NoError(t, result.Err)
	require.NotNil(t, result.CustomStatusCondition)
	assert.Equal(t, ReasonTLSNotConfigured, result.CustomStatusCondition.Reason)

	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(plaintext), &corev1.Pod{}), "the running pod is left alone")
	var updated v1alpha1.Buildkit
	require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(obj), &updated))
	assert.Empty(t, updated.Status.Endpoint, "nothing is advertised for a template that now requires TLS")
}

func TestSetConnectionStatus(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	obj := &v1alpha1.Buildkit{
		Name:      "bk",
		Namespace: "ns",
		Status:    v1alpha1.BuildkitStatus{Endpoint: "tcp://10.0.0.1:1234", ServerCA: "old-cert", ServerName: "bk"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).WithStatusSubresource(obj).Build()
	r := &reconciler{c: &io.ClientApplicator{Client: cl}}
	stored := func() v1alpha1.BuildkitStatus {
		var updated v1alpha1.Buildkit
		require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(obj), &updated))
		return updated.Status
	}

	// A plaintext pod replacing a TLS one: its endpoint, and no pinned certificate.
	require.NoError(t, r.setConnectionStatus(t.Context(), obj, &connection{endpoint: "tcp://10.0.0.2:1234"}))
	got := stored()
	assert.Equal(t, "tcp://10.0.0.2:1234", got.Endpoint)
	assert.Empty(t, got.ServerCA)
	assert.Empty(t, got.ServerName)

	require.NoError(t, r.setConnectionStatus(t.Context(), obj, nil))
	assert.Empty(t, stored().Endpoint)
	assert.Empty(t, obj.Status.Endpoint, "the in-memory object follows, so the FSM's own write doesn't restore it")
}

func TestPodServerCert(t *testing.T) {
	t.Parallel()

	cert, _, err := tlsutil.GenerateServerCert("bk", time.Hour, time.Now())
	require.NoError(t, err)
	pod := func(statuses ...corev1.ContainerStatus) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: statuses}}
	}
	terminated := func(name string, code int32, msg string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Message: msg}}}
	}
	healthProxy := corev1.ContainerStatus{Name: "health-proxy", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}

	got, err := podServerCert(pod(terminated(genCertContainerName, 0, string(cert)), healthProxy), "bk")
	require.NoError(t, err)
	assert.Equal(t, string(cert), got)

	for name, p := range map[string]*corev1.Pod{
		"no status":           pod(healthProxy),
		"still running":       pod(corev1.ContainerStatus{Name: genCertContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}),
		"failed":              pod(terminated(genCertContainerName, 1, string(cert))),
		"wrong name":          pod(terminated(genCertContainerName, 0, string(cert))),
		"another container's": pod(terminated("other", 0, string(cert))),
		"garbage":             pod(terminated(genCertContainerName, 0, "oops")),
	} {
		serverName := "bk"
		if name == "wrong name" {
			serverName = "other-bk"
		}
		_, err := podServerCert(p, serverName)
		assert.Error(t, err, name)
	}
}
