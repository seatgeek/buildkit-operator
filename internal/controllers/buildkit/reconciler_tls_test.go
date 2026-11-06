// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_test

import (
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/reddit/achilles-sdk-api/api"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/controllers/buildkit"
	"github.com/seatgeek/buildkit-operator/internal/test"
	. "github.com/seatgeek/buildkit-operator/internal/test/matchers"
	"github.com/seatgeek/buildkit-operator/internal/tlsutil"
)

func testClientCA() string {
	return test.CACertPEM()
}

// markPodReady makes a pod look running and ready, as kubelet would. For a TLS pod it also has gen-cert
// report a newly minted server certificate, which it returns.
func markPodReady(key client.ObjectKey) string {
	GinkgoHelper()
	var cert string
	setPodStatus(key, func(pod *corev1.Pod) *corev1.ContainerStateTerminated {
		serverName := genCertArg(pod, "--server-name")
		if serverName == "" {
			return nil
		}
		certPEM, _, err := tlsutil.GenerateServerCert(serverName, time.Hour, time.Now())
		Expect(err).NotTo(HaveOccurred())
		cert = string(certPEM)
		return &corev1.ContainerStateTerminated{ExitCode: 0, Message: cert}
	})
	return cert
}

// setPodStatus makes a pod look running and ready, with gen-cert in the state genCert returns (none if
// nil). Envtest has no kubelet, so tests write what it would.
func setPodStatus(key client.ObjectKey, genCert func(*corev1.Pod) *corev1.ContainerStateTerminated) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		var pod corev1.Pod
		g.Expect(c.Get(ctx, key, &pod)).To(Succeed())
		pod.Status.Phase = corev1.PodRunning
		pod.Status.PodIP = "10.0.0.1"
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{Name: "buildkit", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}
		pod.Status.InitContainerStatuses = nil
		if terminated := genCert(&pod); terminated != nil {
			pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
				{Name: "gen-cert", State: corev1.ContainerState{Terminated: terminated}},
			}
		}
		g.Expect(c.Status().Update(ctx, &pod)).To(Succeed())
	}).Should(Succeed())
}

// genCertArg returns the value of a gen-cert flag in the pod's spec, or "" if the pod has no gen-cert.
func genCertArg(pod *corev1.Pod, flag string) string {
	for _, ctr := range pod.Spec.InitContainers {
		if ctr.Name != "gen-cert" {
			continue
		}
		if i := slices.Index(ctr.Command, flag); i >= 0 && i+1 < len(ctr.Command) {
			return ctr.Command[i+1]
		}
	}
	return ""
}

// cleanupNamespace removes the test's objects so their requeues don't slow other specs. The namespace
// itself stays, since envtest never finishes deleting namespaces.
func cleanupNamespace(namespace string) {
	DeferCleanup(func() {
		Expect(c.DeleteAllOf(ctx, &v1alpha1.Buildkit{}, client.InNamespace(namespace))).To(Succeed())
		Expect(c.DeleteAllOf(ctx, &v1alpha1.BuildkitTemplate{}, client.InNamespace(namespace))).To(Succeed())
		Expect(c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(namespace))).To(Succeed())
	})
}

// nudgePod changes a pod annotation so the controller reconciles its Buildkit; re-applying an
// unchanged status would not produce an event.
func nudgePod(key client.ObjectKey) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		var pod corev1.Pod
		g.Expect(c.Get(ctx, key, &pod)).To(Succeed())
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations["test.buildkit.seatgeek.io/nudge"] = time.Now().Format(time.RFC3339Nano)
		g.Expect(c.Update(ctx, &pod)).To(Succeed())
	}).Should(Succeed())
}

var _ = Describe("Buildkit Reconciler with client TLS", func() {
	It("provisions mutual TLS and refuses Buildkits without a client CA", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsNamespace})).To(Succeed())
		cleanupNamespace(tlsNamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "tls-template",
			Namespace: tlsNamespace,
			Spec: v1alpha1.BuildkitTemplateSpec{
				Port:             1234,
				HealthPort:       1235,
				RequireClientTLS: true,
			},
		}
		Expect(c.Create(ctx, template)).To(Succeed())

		By("refusing a Buildkit without spec.clientCA, even without the webhook")
		noCA := &v1alpha1.Buildkit{
			Name:      "no-ca",
			Namespace: tlsNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name},
		}
		Expect(c.Create(ctx, noCA)).To(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(noCA), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed)).To(MatchCondition(api.Condition{
				Status: corev1.ConditionFalse,
				Reason: buildkit.ReasonClientCARequired,
			}))
		}).Should(Succeed())

		By("creating a Buildkit with a client CA")
		clientCA := testClientCA()
		bk := &v1alpha1.Buildkit{
			Name:      "with-ca",
			Namespace: tlsNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: clientCA},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		By("verifying only the TLS pod exists, minting its own certificate, and none for the Buildkit without a CA")
		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())
		pod := pods.Items[0]
		Expect(pod.Spec.Containers[0].Args).To(ContainElement("--tlscacert"))
		Expect(pod.Spec.ActiveDeadlineSeconds).To(Equal(new(v1alpha1.DefaultTLSActiveDeadlineSeconds)), "TLS pods always get a deadline")
		Expect(pod.Spec.InitContainers).To(HaveLen(2))
		genCert := pod.Spec.InitContainers[0]
		Expect(genCert.Name).To(Equal("gen-cert"))
		Expect(genCert.Env).To(ContainElement(corev1.EnvVar{Name: "CLIENT_CA", Value: clientCA}))
		Expect(genCert.TerminationMessagePolicy).To(Equal(corev1.TerminationMessageReadFile))
		Expect(genCertArg(&pod, "--server-name")).To(Equal(bk.Name))
		// Deadline, then the template's grace period (defaulted to 900s by the CRD), then 30 minutes.
		validFor := time.Duration(v1alpha1.DefaultTLSActiveDeadlineSeconds+900)*time.Second + 30*time.Minute
		Expect(genCertArg(&pod, "--valid-for")).To(Equal(validFor.String()))
		var secrets corev1.SecretList
		Expect(c.List(ctx, &secrets, client.InNamespace(tlsNamespace))).To(Succeed())
		Expect(secrets.Items).To(BeEmpty(), "the server key never leaves the pod")

		By("publishing the pod's certificate and name with the endpoint")
		cert := markPodReady(client.ObjectKeyFromObject(&pod))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).To(Equal("tcp://10.0.0.1:1234"))
			g.Expect(updated.Status.ServerCA).To(Equal(cert))
			g.Expect(updated.Status.ServerName).To(Equal(bk.Name))
		}).Should(Succeed())

		By("following the new certificate after the pod sandbox restarts and gen-cert reruns")
		restarted := markPodReady(client.ObjectKeyFromObject(&pod))
		Expect(restarted).NotTo(Equal(cert))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.ServerCA).To(Equal(restarted))
		}).Should(Succeed())

		By("withdrawing the endpoint when the pod vanishes before it is seen terminating")
		Expect(c.Delete(ctx, &pod)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Name).NotTo(Equal(pod.Name))
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).To(BeEmpty(), "no endpoint until the replacement is ready")
			g.Expect(updated.Status.ServerCA).To(BeEmpty())
		}).Should(Succeed())
	})

	It("replaces plaintext pods when the template starts requiring TLS", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsFlipNamespace})).To(Succeed())
		cleanupNamespace(tlsFlipNamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "flip-template",
			Namespace: tlsFlipNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235},
		}
		Expect(c.Create(ctx, template)).To(Succeed())

		bk := &v1alpha1.Buildkit{
			Name:      "plaintext",
			Namespace: tlsFlipNamespace,
			Spec: v1alpha1.BuildkitSpec{
				Template: template.Name,
				ClientCA: testClientCA(),
				// Pod metadata from the Buildkit's creator must not let a plaintext pod pass as TLS.
				Annotations: map[string]string{"buildkit.seatgeek.io/client-tls": "true"},
			},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsFlipNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())
		Expect(pods.Items[0].Spec.Containers[0].Args).NotTo(ContainElement("--tlscacert"))
		plaintextPod := pods.Items[0].Name
		markPodReady(client.ObjectKeyFromObject(&pods.Items[0]))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
			g.Expect(updated.Status.ServerCA).To(BeEmpty())
		}).Should(Succeed())

		By("requiring client TLS on the template")
		Eventually(func(g Gomega) {
			var updated v1alpha1.BuildkitTemplate
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(template), &updated)).To(Succeed())
			updated.Spec.RequireClientTLS = true
			g.Expect(c.Update(ctx, &updated)).To(Succeed())
		}).Should(Succeed())

		By("verifying the plaintext pod is replaced by one that requires client certificates")
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsFlipNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Name).NotTo(Equal(plaintextPod))
			g.Expect(pods.Items[0].Spec.Containers[0].Args).To(ContainElement("--tlscacert"))
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).To(BeEmpty())
		}).Should(Succeed())
	})

	It("advertises nothing when the pod reports no usable certificate", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsCertNamespace})).To(Succeed())
		cleanupNamespace(tlsCertNamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "cert-template",
			Namespace: tlsCertNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())
		bk := &v1alpha1.Buildkit{
			Name:      "bad-cert",
			Namespace: tlsCertNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsCertNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())
		podKey := client.ObjectKeyFromObject(&pods.Items[0])

		By("advertising the pod once it reports a good certificate")
		markPodReady(podKey)
		expectAdvertised := func(advertised bool) {
			GinkgoHelper()
			Eventually(func(g Gomega) {
				var updated v1alpha1.Buildkit
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
				if advertised {
					g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
					return
				}
				g.Expect(updated.Status.Endpoint).To(BeEmpty())
				g.Expect(updated.Status.ServerCA).To(BeEmpty())
				g.Expect(updated.GetCondition(v1alpha1.TypeDeployed).Status).To(Equal(corev1.ConditionFalse))
			}).Should(Succeed())
		}
		expectAdvertised(true)

		otherCert, _, err := tlsutil.GenerateServerCert("someone-else", time.Hour, time.Now())
		Expect(err).NotTo(HaveOccurred())
		for _, tc := range []struct {
			name       string
			terminated *corev1.ContainerStateTerminated
		}{
			{"gen-cert failed", &corev1.ContainerStateTerminated{ExitCode: 1, Message: "boom"}},
			{"a CA certificate", &corev1.ContainerStateTerminated{Message: testClientCA()}},
			{"a certificate for another name", &corev1.ContainerStateTerminated{Message: string(otherCert)}},
			{"no gen-cert status", nil},
		} {
			By("withdrawing it when the pod reports " + tc.name)
			setPodStatus(podKey, func(*corev1.Pod) *corev1.ContainerStateTerminated { return tc.terminated })
			expectAdvertised(false)
			markPodReady(podKey)
			expectAdvertised(true)
		}

		Expect(c.List(ctx, &pods, client.InNamespace(tlsCertNamespace))).To(Succeed())
		Expect(pods.Items).To(HaveLen(1))
		Expect(client.ObjectKeyFromObject(&pods.Items[0])).To(Equal(podKey), "a bad report doesn't restart the pod")
	})

	It("refuses a client CA that isn't a single CA certificate, even without the webhook", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsBadCANamespace})).To(Succeed())
		cleanupNamespace(tlsBadCANamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "bad-ca-template",
			Namespace: tlsBadCANamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())
		bk := &v1alpha1.Buildkit{
			Name:      "bad-ca",
			Namespace: tlsBadCANamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA() + testClientCA()},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed)).To(MatchCondition(api.Condition{
				Status: corev1.ConditionFalse,
				Reason: buildkit.ReasonClientCAInvalid,
			}))
		}).Should(Succeed())
		By("refusing a client CA that has expired")
		expired := &v1alpha1.Buildkit{
			Name:      "expired-ca",
			Namespace: tlsBadCANamespace,
			Spec: v1alpha1.BuildkitSpec{
				Template: template.Name,
				ClientCA: test.CACertPEMValid(time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)),
			},
		}
		Expect(c.Create(ctx, expired)).To(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(expired), &updated)).To(Succeed())
			cond := updated.GetCondition(v1alpha1.TypeDeployed)
			g.Expect(cond.Reason).To(Equal(buildkit.ReasonClientCAInvalid))
			g.Expect(cond.Message).To(ContainSubstring("expired at"))
		}).Should(Succeed())

		By("refusing a client CA that isn't valid yet")
		future := &v1alpha1.Buildkit{
			Name:      "future-ca",
			Namespace: tlsBadCANamespace,
			Spec: v1alpha1.BuildkitSpec{
				Template: template.Name,
				ClientCA: test.CACertPEMValid(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)),
			},
		}
		Expect(c.Create(ctx, future)).To(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(future), &updated)).To(Succeed())
			cond := updated.GetCondition(v1alpha1.TypeDeployed)
			g.Expect(cond.Reason).To(Equal(buildkit.ReasonClientCAInvalid))
			g.Expect(cond.Message).To(ContainSubstring("is not valid until"))
		}).Should(Succeed())

		var pods corev1.PodList
		Consistently(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsBadCANamespace))).To(Succeed())
			g.Expect(pods.Items).To(BeEmpty())
		}, "2s").Should(Succeed())
	})

	It("clears the TLS status once TLS is off and no TLS pod remains", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsOffNamespace})).To(Succeed())
		cleanupNamespace(tlsOffNamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "off-template",
			Namespace: tlsOffNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())
		bk := &v1alpha1.Buildkit{
			Name:      "was-tls",
			Namespace: tlsOffNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsOffNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())
		tlsPod := pods.Items[0]
		markPodReady(client.ObjectKeyFromObject(&tlsPod))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.ServerCA).NotTo(BeEmpty())
		}).Should(Succeed())

		By("keeping the TLS status while the running pod still serves TLS")
		Eventually(func(g Gomega) {
			var updated v1alpha1.BuildkitTemplate
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(template), &updated)).To(Succeed())
			updated.Spec.RequireClientTLS = false
			g.Expect(c.Update(ctx, &updated)).To(Succeed())
		}).Should(Succeed())
		nudgePod(client.ObjectKeyFromObject(&tlsPod))
		Consistently(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.ServerCA).NotTo(BeEmpty())
		}, "2s").Should(Succeed())

		By("following the running TLS pod's new certificate after its sandbox restarts")
		restarted := markPodReady(client.ObjectKeyFromObject(&tlsPod))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.ServerCA).To(Equal(restarted))
		}).Should(Succeed())

		By("clearing it once the replacement pod is plaintext")
		Expect(c.Delete(ctx, &tlsPod)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsOffNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Name).NotTo(Equal(tlsPod.Name))
			g.Expect(pods.Items[0].Spec.Containers[0].Args).NotTo(ContainElement("--tlscacert"))
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).To(BeEmpty(), "no TLS endpoint without its certificate")
			g.Expect(updated.Status.ServerCA).To(BeEmpty())
		}).Should(Succeed())
		markPodReady(client.ObjectKeyFromObject(&pods.Items[0]))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
			g.Expect(updated.Status.ServerCA).To(BeEmpty())
			g.Expect(updated.Status.ServerName).To(BeEmpty())
		}).Should(Succeed())
	})

	It("stops advertising a TLS pod that is terminating, and replaces it once it is gone", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsDrainNamespace})).To(Succeed())
		cleanupNamespace(tlsDrainNamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "drain-template",
			Namespace: tlsDrainNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())
		bk := &v1alpha1.Buildkit{
			Name:      "draining",
			Namespace: tlsDrainNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsDrainNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())
		oldPod := pods.Items[0]
		podKey := client.ObjectKeyFromObject(&oldPod)

		// Envtest has no kubelet, so pods vanish on delete; a finalizer keeps this one terminating.
		const finalizer = "test.buildkit.seatgeek.io/hold"
		Eventually(func(g Gomega) {
			var pod corev1.Pod
			g.Expect(c.Get(ctx, podKey, &pod)).To(Succeed())
			pod.Finalizers = append(pod.Finalizers, finalizer)
			g.Expect(c.Update(ctx, &pod)).To(Succeed())
		}).Should(Succeed())
		releasePod := func() {
			var pod corev1.Pod
			if err := c.Get(ctx, podKey, &pod); err == nil {
				pod.Finalizers = nil
				Expect(c.Update(ctx, &pod)).To(Succeed())
			}
		}
		DeferCleanup(releasePod)
		markPodReady(podKey)
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
		}).Should(Succeed())

		By("withholding the endpoint while the pod, still ready, is terminating")
		Expect(c.Delete(ctx, &oldPod)).To(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed).Reason).To(Equal(api.ConditionReason("PodTerminating")))
			g.Expect(updated.Status.Endpoint).To(BeEmpty())
			g.Expect(updated.Status.ServerCA).To(BeEmpty())
		}).Should(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsDrainNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1), "no replacement until the old pod is gone")
		}, "2s").Should(Succeed())

		By("starting a replacement with its own certificate once the old pod is gone")
		releasePod()
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsDrainNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Name).NotTo(Equal(oldPod.Name))
		}).Should(Succeed())
		cert := markPodReady(client.ObjectKeyFromObject(&pods.Items[0]))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
			g.Expect(updated.Status.ServerCA).To(Equal(cert))
		}).Should(Succeed())
	})

	It("stops advertising a TLS pod that has failed, e.g. at its deadline", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsFailedNamespace})).To(Succeed())
		cleanupNamespace(tlsFailedNamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "failed-template",
			Namespace: tlsFailedNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())
		bk := &v1alpha1.Buildkit{
			Name:      "deadlined",
			Namespace: tlsFailedNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsFailedNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())
		podKey := client.ObjectKeyFromObject(&pods.Items[0])
		markPodReady(podKey)
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
		}).Should(Succeed())

		Eventually(func(g Gomega) {
			var pod corev1.Pod
			g.Expect(c.Get(ctx, podKey, &pod)).To(Succeed())
			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "DeadlineExceeded"
			g.Expect(c.Status().Update(ctx, &pod)).To(Succeed())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed).Reason).To(Equal(api.ConditionReason("PodFailed")))
			g.Expect(updated.Status.Endpoint).To(BeEmpty())
			g.Expect(updated.Status.ServerCA).To(BeEmpty())
			g.Expect(updated.Status.ServerName).To(BeEmpty())
		}).Should(Succeed())
	})
})
