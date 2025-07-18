// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package webhooks

import (
	"context"
	"testing"
	"time"

	"github.com/fgrosse/zaptest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/reddit/achilles-sdk/pkg/logging"
	sdktest "github.com/reddit/achilles-sdk/pkg/test"
	"go.uber.org/zap"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

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

func TestWebhooks(t *testing.T) {
	t.Parallel()

	RegisterFailHandler(Fail)
	ctrllog.SetLogger(ctrlzap.New(ctrlzap.WriteTo(GinkgoWriter), ctrlzap.UseDevMode(true)))
	RunSpecs(t, "Webhook Suite")
}

var _ = BeforeSuite(func() {
	SetDefaultEventuallyTimeout(15 * time.Second)
	SetDefaultEventuallyPollingInterval(100 * time.Millisecond)

	log = zaptest.LoggerWriter(GinkgoWriter).Sugar()
	ctx = logging.NewContext(context.Background(), log) //nolint:fatcontext

	scheme = intscheme.MustNewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(admissionv1.AddToScheme(scheme))

	var err error
	testEnv, err = sdktest.NewEnvTestBuilder(ctx).
		WithCRDDirectoryPaths(test.CRDPaths()).
		WithScheme(scheme).
		WithLog(log.Desugar()).
		WithWebhookConfigs(test.WebhookPath()).
		WithManagerSetupFns(SetupWebhooks).
		Start()

	Expect(err).NotTo(HaveOccurred())

	c = testEnv.Client
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
