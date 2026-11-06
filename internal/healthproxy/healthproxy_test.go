// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package healthproxy

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// startProxy runs the proxy on a listener it already holds, so no other process can take the port, and
// fails the test if the proxy exits with an error.
func startProxy(t *testing.T, socket string) string {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runOn(ctx, lis, socket) }()
	t.Cleanup(func() {
		cancel()
		assert.NoError(t, <-done)
	})

	return lis.Addr().String()
}

func TestRunListenError(t *testing.T) {
	t.Parallel()

	taken, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	require.ErrorContains(t, Run(t.Context(), taken.Addr().String(), "/nonexistent.sock"), "listening on")
}

// startUpstream runs a stand-in for buildkitd's Health service on a unix socket.
func startUpstream(t *testing.T) (*health.Server, string) {
	t.Helper()

	socket := filepath.Join(t.TempDir(), "buildkitd.sock")
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	require.NoError(t, err)

	hs := health.NewServer()
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	return hs, socket
}

func TestHealthProxy(t *testing.T) {
	t.Parallel()

	upstream, socket := startUpstream(t)

	addr := startProxy(t, socket)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := healthpb.NewHealthClient(conn)

	check := func() (healthpb.HealthCheckResponse_ServingStatus, error) {
		resp, err := client.Check(t.Context(), &healthpb.HealthCheckRequest{})
		if err != nil {
			return 0, err
		}
		return resp.GetStatus(), nil
	}

	assert.Eventually(t, func() bool {
		s, err := check()
		return err == nil && s == healthpb.HealthCheckResponse_SERVING
	}, 5*time.Second, 10*time.Millisecond, "reports SERVING while buildkitd does")

	upstream.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	s, err := check()
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, s, "reflects buildkitd's status on every call")

	upstream.Shutdown()
	s, err = check()
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, s)
}

func TestHealthProxyUpstreamDown(t *testing.T) {
	t.Parallel()

	addr := startProxy(t, filepath.Join(t.TempDir(), "missing.sock"))

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	assert.Eventually(t, func() bool {
		_, err := healthpb.NewHealthClient(conn).Check(t.Context(), &healthpb.HealthCheckRequest{})
		return status.Code(err) == codes.Unavailable
	}, 5*time.Second, 10*time.Millisecond, "fails the probe when buildkitd is unreachable")
}

func TestReconnect(t *testing.T) {
	t.Parallel()

	// Unix socket paths are limited to 104 bytes on macOS, and t.TempDir() embeds the test name, so
	// keep this test's name short.
	socket := filepath.Join(t.TempDir(), "buildkitd.sock")
	startAt := func() *grpc.Server {
		lis, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
		require.NoError(t, err)
		srv := grpc.NewServer()
		healthpb.RegisterHealthServer(srv, health.NewServer())
		go srv.Serve(lis)
		return srv
	}
	upstream := startAt()

	addr := startProxy(t, socket)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := healthpb.NewHealthClient(conn)
	serving := func() bool {
		resp, err := client.Check(t.Context(), &healthpb.HealthCheckRequest{})
		return err == nil && resp.GetStatus() == healthpb.HealthCheckResponse_SERVING
	}

	require.Eventually(t, serving, 5*time.Second, 10*time.Millisecond)

	// Keep buildkitd down long enough for several failed reconnects to grow the backoff.
	upstream.Stop()
	require.Eventually(t, func() bool { return !serving() }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(10 * time.Second)

	upstream = startAt()
	t.Cleanup(upstream.Stop)
	restarted := time.Now()
	require.Eventually(t, serving, 5*time.Second, 50*time.Millisecond)
	assert.Less(t, time.Since(restarted), 2500*time.Millisecond, "probe should recover within about a second of buildkitd")
}
