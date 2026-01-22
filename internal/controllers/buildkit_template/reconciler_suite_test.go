// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_template_test

import (
	"context"
	"testing"
	"time"

	"github.com/fgrosse/zaptest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/reddit/achilles-sdk/pkg/fsm/metrics"
	"github.com/reddit/achilles-sdk/pkg/io"
	"github.com/reddit/achilles-sdk/pkg/logging"
	achratelimiter "github.com/reddit/achilles-sdk/pkg/ratelimiter"
	sdktest "github.com/reddit/achilles-sdk/pkg/test"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit_template"
	"github.com/seatgeek/buildkit-operator/internal/controlplane"
	intscheme "github.com/seatgeek/buildkit-operator/internal/scheme"
	"github.com/seatgeek/buildkit-operator/internal/test"
)

var (
	ctx     context.Context
	testEnv *sdktest.TestEnv
	c       client.Client
	scheme  *runtime.Scheme
	log     *zap.SugaredLogger
)

func TestBuildkitTemplateReconciler(t *testing.T) {
	t.Parallel()

	RegisterFailHandler(Fail)
	ctrllog.SetLogger(ctrlzap.New(ctrlzap.WriteTo(GinkgoWriter), ctrlzap.UseDevMode(true)))
	RunSpecs(t, "BuildkitTemplate Reconciler Suite")
}

var _ = BeforeSuite(func() {
	SetDefaultEventuallyTimeout(15 * time.Second)
	SetDefaultEventuallyPollingInterval(100 * time.Millisecond)

	log = zaptest.LoggerWriter(GinkgoWriter).Sugar()
	ctx = logging.NewContext(context.Background(), log) //nolint:fatcontext
	rl := achratelimiter.NewDefaultProviderRateLimiter(achratelimiter.DefaultProviderRPS)

	scheme = intscheme.MustNewScheme()

	var err error
	testEnv, err = sdktest.NewEnvTestBuilder(ctx).
		WithCRDDirectoryPaths(test.CRDPaths()).
		WithScheme(scheme).
		WithLog(log.Desugar()).
		WithManagerSetupFns(
			func(mgr manager.Manager) error {
				clientApplicator := &io.ClientApplicator{
					Client:     mgr.GetClient(),
					Applicator: io.NewAPIPatchingApplicator(mgr.GetClient()),
				}

				cpCtx := controlplane.Context{
					Metrics: metrics.MustMakeMetrics(scheme, prometheus.NewRegistry()),
				}

				return buildkit_template.SetupController(ctx, cpCtx, mgr, rl, clientApplicator)
			},
		).
		Start()

	Expect(err).NotTo(HaveOccurred())

	c = testEnv.Client
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
