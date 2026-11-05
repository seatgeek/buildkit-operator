// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

// Package healthproxy serves grpc.health.v1 over plaintext TCP by forwarding each check to buildkitd's
// own Health service on its local unix socket. This lets kubelet gRPC probes keep working when
// buildkitd's TCP listener requires mutual TLS, which kubelet probes cannot present. Only the Health
// service is exposed, so the plaintext port reveals nothing beyond up/down.
package healthproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type server struct {
	healthpb.UnimplementedHealthServer

	upstream healthpb.HealthClient
}

// Check is answered live by buildkitd on every call so the probe reflects the daemon, not this proxy.
func (s *server) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return s.upstream.Check(ctx, req)
}

// Run serves until ctx is cancelled. socketPath is the absolute path of buildkitd's unix socket.
func Run(ctx context.Context, listenAddr, socketPath string) error {
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// gRPC's default reconnect backoff grows to 120s, during which checks fail fast. After
		// buildkitd restarts that would report it down long after it recovered and exhaust the startup
		// probe. The socket is local, so retrying every second at most is cheap.
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 100 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: time.Second},
			MinConnectTimeout: time.Second,
		}),
	)
	if err != nil {
		return fmt.Errorf("creating buildkitd client: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", listenAddr, err)
	}

	return serve(ctx, lis, conn)
}

func serve(ctx context.Context, lis net.Listener, upstream grpc.ClientConnInterface) error {
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, &server{upstream: healthpb.NewHealthClient(upstream)})

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}

	return nil
}
