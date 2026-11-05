// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/reddit/achilles-sdk-api/api"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func markPodReady(key client.ObjectKey) {
	Eventually(func(g Gomega) {
		var pod corev1.Pod
		g.Expect(c.Get(ctx, key, &pod)).To(Succeed())
		pod.Status.Phase = corev1.PodRunning
		pod.Status.PodIP = "10.0.0.1"
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{
			{Name: "buildkit", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}
		g.Expect(c.Status().Update(ctx, &pod)).To(Succeed())
	}).Should(Succeed())
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

		By("verifying the TLS secret is owned by the Buildkit and carries its client CA")
		var secret corev1.Secret
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, client.ObjectKey{Namespace: tlsNamespace, Name: "with-ca-tls"}, &secret)).To(Succeed())
		}).Should(Succeed())
		var created v1alpha1.Buildkit
		Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &created)).To(Succeed())
		Expect(metav1.IsControlledBy(&secret, &created)).To(BeTrue())
		Expect(secret.Labels).To(HaveKeyWithValue(buildkit.TLSSecretLabel, "true"))
		Expect(string(secret.Data[buildkit.SecretClientCAKey])).To(Equal(clientCA))
		Expect(secret.Data[corev1.TLSPrivateKeyKey]).NotTo(BeEmpty())

		By("verifying only the TLS-configured pod exists, and none for the Buildkit without a CA")
		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())
		pod := pods.Items[0]
		Expect(pod.Spec.Containers[0].Args).To(ContainElement("--tlscacert"))
		fingerprint, err := tlsutil.Fingerprint(secret.Data[corev1.TLSCertKey])
		Expect(err).NotTo(HaveOccurred())
		Expect(pod.Annotations).To(HaveKeyWithValue(buildkit.ServerCertAnnotation, fingerprint))
		Expect(pod.Spec.InitContainers).To(HaveLen(1))

		By("publishing the server certificate and name with the endpoint")
		markPodReady(client.ObjectKeyFromObject(&pod))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).To(Equal("tcp://10.0.0.1:1234"))
			g.Expect(updated.Status.ServerCA).To(Equal(string(secret.Data[corev1.TLSCertKey])))
			g.Expect(updated.Status.ServerName).To(Equal(bk.Name))
		}).Should(Succeed())

		By("restarting the pod when its Secret is replaced, so status matches the served certificate")
		Expect(c.Delete(ctx, &secret)).To(Succeed())
		markPodReady(client.ObjectKeyFromObject(&pod)) // any pod event triggers a reconcile
		Eventually(func(g Gomega) {
			var replaced corev1.Secret
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(&secret), &replaced)).To(Succeed())
			g.Expect(replaced.Data[corev1.TLSCertKey]).NotTo(Equal(secret.Data[corev1.TLSCertKey]))
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Name).NotTo(Equal(pod.Name))
		}).Should(Succeed())
		var replaced corev1.Secret
		Expect(c.Get(ctx, client.ObjectKeyFromObject(&secret), &replaced)).To(Succeed())
		markPodReady(client.ObjectKeyFromObject(&pods.Items[0]))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
			g.Expect(updated.Status.ServerCA).To(Equal(string(replaced.Data[corev1.TLSCertKey])))
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

	It("refuses Secrets it doesn't own and replaces ones it can't vouch for", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsSecretNamespace})).To(Succeed())
		cleanupNamespace(tlsSecretNamespace)
		DeferCleanup(func() {
			Expect(c.DeleteAllOf(ctx, &corev1.Secret{}, client.InNamespace(tlsSecretNamespace))).To(Succeed())
		})

		template := &v1alpha1.BuildkitTemplate{
			Name:      "secret-template",
			Namespace: tlsSecretNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())

		By("refusing, without deleting, a pre-existing Secret with the instance's name that it doesn't own")
		squattedSecret := &corev1.Secret{
			Name:      "squatted-tls",
			Namespace: tlsSecretNamespace,
			Data:      map[string][]byte{corev1.TLSCertKey: []byte("x"), corev1.TLSPrivateKeyKey: []byte("x"), buildkit.SecretClientCAKey: []byte("x")},
		}
		Expect(c.Create(ctx, squattedSecret)).To(Succeed())
		squatted := &v1alpha1.Buildkit{
			Name:      "squatted",
			Namespace: tlsSecretNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, squatted)).To(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(squatted), &updated)).To(Succeed())
			cond := updated.GetCondition(v1alpha1.TypeDeployed)
			g.Expect(cond.Status).NotTo(Equal(corev1.ConditionTrue))
			g.Expect(cond.Message).To(ContainSubstring("TLS secret for this Buildkit is unusable"))
			g.Expect(cond.Message).NotTo(ContainSubstring("not controlled"))
			g.Expect(updated.Status.ServerCA).To(BeEmpty())
		}).Should(Succeed())
		var stillThere corev1.Secret
		Expect(c.Get(ctx, client.ObjectKeyFromObject(squattedSecret), &stillThere)).To(Succeed())
		Expect(stillThere.UID).To(Equal(squattedSecret.UID))

		// startTLS creates a Buildkit and waits for its operator-made Secret.
		startTLS := func(name string) (*v1alpha1.Buildkit, corev1.Secret) {
			GinkgoHelper()
			bk := &v1alpha1.Buildkit{
				Name:      name,
				Namespace: tlsSecretNamespace,
				Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
			}
			Expect(c.Create(ctx, bk)).To(Succeed())
			var secret corev1.Secret
			Eventually(func(g Gomega) {
				g.Expect(c.Get(ctx, client.ObjectKey{Namespace: tlsSecretNamespace, Name: name + "-tls"}, &secret)).To(Succeed())
				var updated v1alpha1.Buildkit
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
				g.Expect(updated.Status.ServerCertSHA256).NotTo(BeEmpty())
			}).Should(Succeed())
			return bk, secret
		}

		// expectReplaced waits for the operator to swap the Secret for a fresh one it can vouch for.
		expectReplaced := func(bk *v1alpha1.Buildkit, previous corev1.Secret) {
			GinkgoHelper()
			Expect(c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(tlsSecretNamespace))).To(Succeed()) // trigger a reconcile
			Eventually(func(g Gomega) {
				var secret corev1.Secret
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(&previous), &secret)).To(Succeed())
				g.Expect(secret.UID).NotTo(Equal(previous.UID))
				g.Expect(string(secret.Data[buildkit.SecretClientCAKey])).To(Equal(bk.Spec.ClientCA))
				g.Expect(tlsutil.ValidateServerKeyPair(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey], bk.Name)).To(Succeed())

				var updated v1alpha1.Buildkit
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
				fingerprint, err := tlsutil.Fingerprint(secret.Data[corev1.TLSCertKey])
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(updated.Status.ServerCertSHA256).To(Equal(fingerprint))

				var pods corev1.PodList
				g.Expect(c.List(ctx, &pods, client.InNamespace(tlsSecretNamespace))).To(Succeed())
				for _, pod := range pods.Items {
					if pod.GenerateName == bk.Name+"-" {
						g.Expect(pod.Annotations).To(HaveKeyWithValue(buildkit.ServerCertAnnotation, fingerprint))
					}
				}
			}).Should(Succeed())
		}

		By("replacing a Secret with a forged owner reference and someone else's server key")
		forged, original := startTLS("forged")
		Expect(c.Delete(ctx, &original)).To(Succeed())
		attackerCert, attackerKey, err := tlsutil.GenerateServerCert("forged", time.Hour, time.Now())
		Expect(err).NotTo(HaveOccurred())
		impostor := &corev1.Secret{
			Name:            original.Name,
			Namespace:       original.Namespace,
			OwnerReferences: original.OwnerReferences,
			Type:            corev1.SecretTypeTLS,
			Data: map[string][]byte{
				corev1.TLSCertKey:          attackerCert,
				corev1.TLSPrivateKeyKey:    attackerKey,
				buildkit.SecretClientCAKey: []byte(forged.Spec.ClientCA),
			},
		}
		Expect(c.Create(ctx, impostor)).To(Succeed())
		expectReplaced(forged, *impostor)

		By("replacing its own Secret once the client CA no longer matches the spec")
		tampered, tamperedSecret := startTLS("tampered")
		tamperedSecret.Data[buildkit.SecretClientCAKey] = []byte(testClientCA())
		Expect(c.Update(ctx, &tamperedSecret)).To(Succeed())
		expectReplaced(tampered, tamperedSecret)

		By("replacing its own Secret once the server key is unusable")
		keyless, keylessSecret := startTLS("keyless")
		// Type kubernetes.io/tls requires the key to be present, but not non-empty.
		keylessSecret.Data[corev1.TLSPrivateKeyKey] = []byte{}
		Expect(c.Update(ctx, &keylessSecret)).To(Succeed())
		expectReplaced(keyless, keylessSecret)
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
		var pods corev1.PodList
		Consistently(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsBadCANamespace))).To(Succeed())
			g.Expect(pods.Items).To(BeEmpty())
			g.Expect(c.Get(ctx, client.ObjectKey{Namespace: tlsBadCANamespace, Name: "bad-ca-tls"}, &corev1.Secret{})).NotTo(Succeed())
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

	It("doesn't publish the endpoint of a TLS pod that is terminating", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsTerminatingNamespace})).To(Succeed())
		cleanupNamespace(tlsTerminatingNamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "terminating-template",
			Namespace: tlsTerminatingNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())
		bk := &v1alpha1.Buildkit{
			Name:      "restarting",
			Namespace: tlsTerminatingNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsTerminatingNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())
		oldPod := pods.Items[0]
		oldKey := client.ObjectKeyFromObject(&oldPod)

		// Envtest has no kubelet, so pods vanish on delete; a finalizer keeps this one terminating.
		const finalizer = "test.buildkit.seatgeek.io/hold"
		Eventually(func(g Gomega) {
			var pod corev1.Pod
			g.Expect(c.Get(ctx, oldKey, &pod)).To(Succeed())
			pod.Finalizers = append(pod.Finalizers, finalizer)
			g.Expect(c.Update(ctx, &pod)).To(Succeed())
		}).Should(Succeed())
		markPodReady(oldKey)
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
		}).Should(Succeed())

		By("replacing the Secret, which makes the controller restart the pod")
		Expect(c.Delete(ctx, &corev1.Secret{Name: "restarting-tls", Namespace: tlsTerminatingNamespace})).To(Succeed())
		nudgePod(oldKey)

		By("withholding the endpoint while the old pod, still ready, is terminating")
		Eventually(func(g Gomega) {
			var pod corev1.Pod
			g.Expect(c.Get(ctx, oldKey, &pod)).To(Succeed())
			g.Expect(pod.DeletionTimestamp).NotTo(BeNil())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed).Reason).To(Equal(api.ConditionReason("PodTerminating")))
		}).Should(Succeed())
		Consistently(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).To(BeEmpty())
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsTerminatingNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1), "no replacement until the old pod is gone")
		}, "2s").Should(Succeed())

		By("starting a replacement once the old pod is gone")
		Eventually(func(g Gomega) {
			var pod corev1.Pod
			g.Expect(c.Get(ctx, oldKey, &pod)).To(Succeed())
			pod.Finalizers = nil
			g.Expect(c.Update(ctx, &pod)).To(Succeed())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsTerminatingNamespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Name).NotTo(Equal(oldPod.Name))
		}).Should(Succeed())
		var secret corev1.Secret
		Expect(c.Get(ctx, client.ObjectKey{Namespace: tlsTerminatingNamespace, Name: "restarting-tls"}, &secret)).To(Succeed())
		markPodReady(client.ObjectKeyFromObject(&pods.Items[0]))
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
			g.Expect(updated.Status.ServerCA).To(Equal(string(secret.Data[corev1.TLSCertKey])))
		}).Should(Succeed())
	})

	It("reports a clear error and starts no pod in a namespace not configured for TLS", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsUnconfiguredNamespace})).To(Succeed())
		cleanupNamespace(tlsUnconfiguredNamespace)

		template := &v1alpha1.BuildkitTemplate{
			Name:      "unconfigured-template",
			Namespace: tlsUnconfiguredNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())
		bk := &v1alpha1.Buildkit{
			Name:      "unconfigured",
			Namespace: tlsUnconfiguredNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, bk)).To(Succeed())

		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			cond := updated.GetCondition(v1alpha1.TypeDeployed)
			g.Expect(cond.Status).NotTo(Equal(corev1.ConditionTrue))
			g.Expect(cond.Message).To(ContainSubstring("not configured to manage TLS secrets in namespace " + tlsUnconfiguredNamespace))
		}).Should(Succeed())

		var pods corev1.PodList
		Consistently(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsUnconfiguredNamespace))).To(Succeed())
			g.Expect(pods.Items).To(BeEmpty())
		}, "2s").Should(Succeed())
	})
})
