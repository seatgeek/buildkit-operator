// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/reddit/achilles-sdk/pkg/bootstrap"
	"github.com/reddit/achilles-sdk/pkg/fsm/metrics"
	"github.com/reddit/achilles-sdk/pkg/io"
	"github.com/reddit/achilles-sdk/pkg/logging"
	"github.com/reddit/achilles-sdk/pkg/meta"
	"github.com/reddit/achilles-sdk/pkg/ratelimiter"
	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit_template"
	"github.com/seatgeek/buildkit-operator/internal/controlplane"
	intscheme "github.com/seatgeek/buildkit-operator/internal/scheme"
	"github.com/seatgeek/buildkit-operator/internal/webhooks"
)

// opts store any optional settings that instruct how the manager and
// controllers should run. Typically these are fed values from CLI flags or
// environment variables.
type opts struct {
	bootstrap bootstrap.Options
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
		promReg := prometheus.NewRegistry()
		promMetrics := metrics.MustMakeMetrics(mgr.GetScheme(), promReg)

		// map flag values into controlplane's context
		cpCtx := controlplane.Context{
			Metrics: promMetrics,
		}
		log, err := logging.FromContext(ctx)
		if err != nil {
			return fmt.Errorf("getting logger from context: %w", err)
		}

		log.Info("starting controllers...")

		rl := ratelimiter.NewDefaultProviderRateLimiter(5)
		if err := buildkit.SetupController(ctx, cpCtx, mgr, rl, client); err != nil {
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
