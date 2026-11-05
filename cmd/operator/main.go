// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/reddit/achilles-sdk/pkg/bootstrap"
	"github.com/reddit/achilles-sdk/pkg/fsm/metrics"
	"github.com/reddit/achilles-sdk/pkg/io"
	"github.com/reddit/achilles-sdk/pkg/logging"
	"github.com/reddit/achilles-sdk/pkg/meta"
	"github.com/reddit/achilles-sdk/pkg/ratelimiter"
	"github.com/spf13/cobra"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	crtMetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit_template"
	"github.com/seatgeek/buildkit-operator/internal/controlplane"
	"github.com/seatgeek/buildkit-operator/internal/healthproxy"
	intscheme "github.com/seatgeek/buildkit-operator/internal/scheme"
	"github.com/seatgeek/buildkit-operator/internal/webhooks"
)

// opts store any optional settings that instruct how the manager and
// controllers should run. Typically these are fed values from CLI flags or
// environment variables.
type opts struct {
	bootstrap bootstrap.Options

	tlsSecretNamespaces []string
	healthProxyImage    string
}

const (
	ApplicationName = "buildkit-operator"
	ComponentName   = "buildkit-operator"
)

// Version is dynamically set at compile time
var Version = "0.0.1"

func main() {
	ctx := context.Background()
	if err := rootCommand(ctx).Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %s\n", err)
		os.Exit(1)
	}
}

func rootCommand(ctx context.Context) *cobra.Command {
	o := &opts{}

	cmd := &cobra.Command{
		Use:     "buildkit-operator",
		Version: Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return bootstrap.Start(ctx,
				intscheme.AddToSchemes,
				&o.bootstrap,
				initStartFunc(o))
		},
	}

	o.bootstrap.AddToFlags(cmd.Flags())
	cmd.Flags().StringSliceVar(&o.tlsSecretNamespaces, "tls-secret-namespaces", nil, "Namespaces where the operator may create TLS secrets for BuildkitTemplates that set requireClientTLS")
	cmd.Flags().StringVar(&o.healthProxyImage, "health-proxy-image", "", "Image for the health-proxy sidecar used with requireClientTLS; normally the operator's own image")

	cmd.AddCommand(healthProxyCommand(ctx))

	return cmd
}

// healthProxyCommand runs the health-only sidecar injected into Buildkit pods that require client TLS.
func healthProxyCommand(ctx context.Context) *cobra.Command {
	var listen, socket string

	cmd := &cobra.Command{
		Use:   "health-proxy",
		Short: "Serve grpc.health.v1 in plaintext by forwarding to buildkitd's unix socket",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return healthproxy.Run(ctx, listen, socket)
		},
	}

	cmd.Flags().StringVar(&listen, "listen", ":1235", "TCP address to serve health checks on")
	cmd.Flags().StringVar(&socket, "socket", "", "Absolute path of buildkitd's unix socket")
	_ = cmd.MarkFlagRequired("socket")

	return cmd
}

// initStartFunc accepts options that are typically set from CLI flags or
// environment variables. It returns an instance of [bootstrap.StartFunc],
// which can then be fed into [bootstrap.Start].
func initStartFunc(o *opts) bootstrap.StartFunc {
	return func(ctx context.Context, mgr manager.Manager) error {
		meta.InitRedditLabels(ApplicationName, Version, ComponentName)

		client := &io.ClientApplicator{
			Client:     mgr.GetClient(),
			Applicator: io.NewAPIPatchingApplicator(mgr.GetClient()),
		}

		// metrics sink
		promMetrics := metrics.MustMakeMetrics(mgr.GetScheme(), crtMetrics.Registry)

		// map flag values into controlplane's context
		cpCtx := controlplane.Context{
			Metrics: promMetrics,
		}
		log, err := logging.FromContext(ctx)
		if err != nil {
			return fmt.Errorf("getting logger from context: %w", err)
		}

		log.Info("starting controllers...")

		// Uncached so the operator never lists or caches Secrets; its RBAC only allows get/create in
		// the TLS namespaces.
		secretClient, err := crclient.New(mgr.GetConfig(), crclient.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
		if err != nil {
			return fmt.Errorf("creating secret client: %w", err)
		}
		tlsConfig := buildkit.TLSConfig{
			SecretClient:     secretClient,
			Namespaces:       o.tlsSecretNamespaces,
			HealthProxyImage: o.healthProxyImage,
		}

		rl := ratelimiter.NewDefaultProviderRateLimiter(5)
		if err := buildkit.SetupController(ctx, cpCtx, mgr, rl, client, tlsConfig); err != nil {
			return fmt.Errorf("failed to setup Buildkit controller: %w", err)
		}
		if err := buildkit_template.SetupController(ctx, cpCtx, mgr, rl, client); err != nil {
			return fmt.Errorf("failed to setup BuildkitTemplate controller: %w", err)
		}

		if err := webhooks.SetupWebhooks(mgr); err != nil {
			return fmt.Errorf("failed to setup webhooks: %w", err)
		}

		return nil
	}
}
