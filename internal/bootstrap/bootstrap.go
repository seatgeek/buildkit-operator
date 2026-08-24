// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

// Package bootstrap starts the controller manager the same way
// github.com/reddit/achilles-sdk/pkg/bootstrap does, but with the informer
// cache scoped to the objects this operator manages. The SDK's bootstrap
// offers no way to configure cache.Options, so its manager caches every Pod
// and ConfigMap in the cluster, growing the operator's memory footprint with
// total cluster size rather than with the number of Buildkit instances.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/zapr"
	"github.com/iancoleman/strcase"
	sdkbootstrap "github.com/reddit/achilles-sdk/pkg/bootstrap"
	"github.com/reddit/achilles-sdk/pkg/logging"
	"github.com/reddit/achilles-sdk/pkg/meta"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	zaputil "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

const (
	errNoValidKubeContext      = "kubeconfig context must be specified when not in cluster"
	errKubeContextSetInCluster = "kubeconfig context can not be specified when in cluster"
)

// Start runs the controller manager. It mirrors
// [sdkbootstrap.Start] except that the manager is built with [CacheOptions].
func Start(
	ctx context.Context,
	schemes runtime.SchemeBuilder,
	opts *sdkbootstrap.Options,
	startFunc sdkbootstrap.StartFunc,
) error {
	log := setupLogging(opts.VerboseMode, opts.DevLogger)
	ctx = logging.NewContext(ctx, log)

	cfg, err := buildRestConfig(opts)
	if err != nil {
		return fmt.Errorf("building k8s client config: %w", err)
	}

	mgr, err := buildManager(cfg, log, schemes, opts)
	if err != nil {
		return fmt.Errorf("building manager: %w", err)
	}

	if err := startFunc(ctx, mgr); err != nil {
		return fmt.Errorf("running start func: %w", err)
	}

	log.Info("starting manager")
	// the signal handler context is deliberately the manager's root context
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil { //nolint:contextcheck
		return fmt.Errorf("starting manager: %w", err)
	}

	return nil
}

// CacheOptions scopes the manager's informer cache for the built-in types the
// operator watches. The achilles-sdk FSM labels every object it applies with
// [meta.ManagedByKey] set to the controller's name, so filtering on that
// label keeps only the operator's own Pods and ConfigMaps in the cache. The
// Buildkit and BuildkitTemplate CRDs remain unfiltered.
//
// A nil syncPeriod uses the controller-runtime default.
func CacheOptions(syncPeriod *time.Duration) cache.Options {
	return cache.Options{
		SyncPeriod: syncPeriod,
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}:       {Label: managedBySelector(v1alpha1.Buildkit{})},
			&corev1.ConfigMap{}: {Label: managedBySelector(v1alpha1.BuildkitTemplate{})},
		},
	}
}

// managedBySelector matches objects applied by the FSM controller that
// reconciles the given resource type. The achilles-sdk names each controller
// as the kebab-cased Kind of its reconciled resource and stamps that name
// into the [meta.ManagedByKey] label of every object the controller applies.
func managedBySelector(reconciledType any) labels.Selector { //nolint:ireturn
	return labels.SelectorFromSet(labels.Set{
		meta.ManagedByKey: strcase.ToKebab(reflect.TypeOf(reconciledType).Name()),
	})
}

//nolint:ireturn
func buildManager(
	cfg *rest.Config,
	log *zap.SugaredLogger,
	schemes runtime.SchemeBuilder,
	opts *sdkbootstrap.Options,
) (manager.Manager, error) {
	mgr, err := manager.New(
		cfg,
		manager.Options{
			HealthProbeBindAddress:  opts.HealthAddr,
			Metrics:                 server.Options{BindAddress: opts.MetricsAddr},
			Logger:                  zapr.NewLogger(log.Desugar()),
			Cache:                   CacheOptions(&opts.SyncPeriod),
			LeaderElection:          opts.LeaderElection,
			LeaderElectionID:        opts.LeaderElectionID,
			LeaderElectionNamespace: opts.LeaderElectionNamespace,
			RenewDeadline:           &opts.LeaderElectionRenewDeadline,
			LeaseDuration:           &opts.LeaderElectionLeaseDuration,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("constructing manager: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding healthz: %w", err)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding readyz: %w", err)
	}

	if schemes != nil {
		if err := schemes.AddToScheme(mgr.GetScheme()); err != nil {
			return nil, err
		}
	}
	return mgr, nil
}

func buildRestConfig(o *sdkbootstrap.Options) (*rest.Config, error) {
	if o.InCluster {
		if o.KubeContext != "" {
			return nil, errors.New(errKubeContextSetInCluster)
		}

		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("building in-cluster kubeconfig: %w", err)
		}

		cfg.QPS = o.ClientQPS
		cfg.Burst = o.ClientBurst

		return cfg, err
	}

	if o.KubeContext == "" {
		return nil, errors.New(errNoValidKubeContext)
	}

	var rules *clientcmd.ClientConfigLoadingRules
	if o.KubeConfig != "" {
		rules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: o.KubeConfig}
	} else {
		rules = clientcmd.NewDefaultClientConfigLoadingRules()
	}

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{
			CurrentContext: o.KubeContext,
		},
	).ClientConfig()
	if err != nil {
		return nil, err
	}

	cfg.QPS = o.ClientQPS
	cfg.Burst = o.ClientBurst

	return cfg, nil
}

func setupLogging(verboseMode, devLogger bool) *zap.SugaredLogger {
	var baseLogger *zap.Logger
	if devLogger {
		l, err := zap.NewDevelopment()
		if err != nil {
			panic(err)
		}
		baseLogger = l
	} else {
		level := zapcore.InfoLevel
		if verboseMode {
			level = zapcore.DebugLevel
		}
		atomicLevel := zap.NewAtomicLevelAt(level)
		baseLogger = zaputil.NewRaw(
			zaputil.Level(&atomicLevel),
			func(options *zaputil.Options) {
				options.TimeEncoder = zapcore.ISO8601TimeEncoder
			},
		)
	}

	// controller-runtime requires its global logger to be set before any
	// controllers are built
	ctrl.SetLogger(zapr.NewLogger(baseLogger))

	return baseLogger.Sugar()
}
