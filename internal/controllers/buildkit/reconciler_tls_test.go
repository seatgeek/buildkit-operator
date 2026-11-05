// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_test

import (
	"maps"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/reddit/achilles-sdk-api/api"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

// currentSecret returns the TLS Secret named for the Buildkit's recorded server certificate.
func currentSecret(g Gomega, bk *v1alpha1.Buildkit) corev1.Secret {
	var updated v1alpha1.Buildkit
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
	fingerprint := updated.Status.ServerCertSHA256
	g.Expect(fingerprint).To(HaveLen(64))
	var secret corev1.Secret
	g.Expect(c.Get(ctx, client.ObjectKey{Namespace: bk.Namespace, Name: bk.Name + "-tls-" + fingerprint[:16]}, &secret)).To(Succeed())
	return secret
}

// mountedSecret returns the TLS Secret a pod's buildkitd mounts.
func mountedSecret(pod corev1.Pod) string {
	for _, v := range pod.Spec.Volumes {
		if v.Name == "buildkitd-tls" && v.Secret != nil {
			return v.Secret.SecretName
		}
	}
	return ""
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
		Eventually(func(g Gomega) { secret = currentSecret(g, bk) }).Should(Succeed())
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
		Expect(mountedSecret(pod)).To(Equal(secret.Name))
		Expect(secret.Immutable).NotTo(BeNil())
		Expect(*secret.Immutable).To(BeTrue())
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

		// expectRotated waits for a new certificate in a new Secret, mounted by a new pod, and for status
		// to advertise it once that pod is ready.
		expectRotated := func(previous corev1.Secret, previousPod string) (corev1.Secret, corev1.Pod) {
			GinkgoHelper()
			var rotated corev1.Secret
			Eventually(func(g Gomega) {
				rotated = currentSecret(g, bk)
				g.Expect(rotated.Name).NotTo(Equal(previous.Name))
				g.Expect(c.List(ctx, &pods, client.InNamespace(tlsNamespace))).To(Succeed())
				g.Expect(pods.Items).To(HaveLen(1))
				g.Expect(pods.Items[0].Name).NotTo(Equal(previousPod))
				g.Expect(mountedSecret(pods.Items[0])).To(Equal(rotated.Name))
			}).Should(Succeed())
			newPod := pods.Items[0]
			markPodReady(client.ObjectKeyFromObject(&newPod))
			Eventually(func(g Gomega) {
				var updated v1alpha1.Buildkit
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
				g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
				g.Expect(updated.Status.ServerCA).To(Equal(string(rotated.Data[corev1.TLSCertKey])))
			}).Should(Succeed())
			return rotated, newPod
		}

		By("minting a new certificate and restarting the pod when its Secret disappears")
		Expect(c.Delete(ctx, &secret)).To(Succeed())
		nudgePod(client.ObjectKeyFromObject(&pod))
		rotated, rotatedPod := expectRotated(secret, pod.Name)

		By("deleting the superseded Secret when re-minting while the old one still exists")
		// Stands in for any re-mint (e.g. renewal) of a still-valid certificate.
		Expect(c.Status().Patch(ctx, &v1alpha1.Buildkit{Name: bk.Name, Namespace: bk.Namespace},
			client.RawPatch(types.MergePatchType, []byte(`{"status":{"serverCertSHA256":null}}`)))).To(Succeed())
		nudgePod(client.ObjectKeyFromObject(&rotatedPod))
		expectRotated(rotated, rotatedPod.Name)
		Eventually(func(g Gomega) {
			g.Expect(apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(&rotated), &corev1.Secret{}))).To(BeTrue())
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
			Eventually(func(g Gomega) { secret = currentSecret(g, bk) }).Should(Succeed())
			return bk, secret
		}

		// expectReplaced waits for the operator to move to a fresh Secret it can vouch for, under a new name.
		// previousGone says whether the operator should have deleted the previous Secret, which it only
		// does for one claiming this Buildkit as owner.
		expectReplaced := func(bk *v1alpha1.Buildkit, previous corev1.Secret, previousGone bool) {
			GinkgoHelper()
			Expect(c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(tlsSecretNamespace))).To(Succeed()) // trigger a reconcile
			Eventually(func(g Gomega) {
				secret := currentSecret(g, bk)
				g.Expect(secret.Name).NotTo(Equal(previous.Name))
				g.Expect(string(secret.Data[buildkit.SecretClientCAKey])).To(Equal(bk.Spec.ClientCA))
				g.Expect(tlsutil.ValidateServerKeyPair(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey], bk.Name)).To(Succeed())

				var old corev1.Secret
				err := c.Get(ctx, client.ObjectKeyFromObject(&previous), &old)
				if previousGone {
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unvouched Secret claiming this Buildkit is deleted")
				} else {
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(old.UID).To(Equal(previous.UID), "Secret not claiming this Buildkit is left alone")
				}

				var pods corev1.PodList
				g.Expect(c.List(ctx, &pods, client.InNamespace(tlsSecretNamespace))).To(Succeed())
				for _, pod := range pods.Items {
					if pod.GenerateName == bk.Name+"-" {
						g.Expect(mountedSecret(pod)).To(Equal(secret.Name))
					}
				}
			}).Should(Succeed())
		}

		By("leaving alone a Secret at its current name that doesn't claim it, and minting a new one")
		squatted, squattedOriginal := startTLS("squatted")
		Expect(c.Delete(ctx, &squattedOriginal)).To(Succeed())
		foreign := &corev1.Secret{
			Name:      squattedOriginal.Name,
			Namespace: tlsSecretNamespace,
			Data:      map[string][]byte{corev1.TLSCertKey: []byte("x"), corev1.TLSPrivateKeyKey: []byte("x"), buildkit.SecretClientCAKey: []byte("x")},
		}
		Expect(c.Create(ctx, foreign)).To(Succeed())
		expectReplaced(squatted, *foreign, false)

		By("replacing a Secret with a forged owner reference and someone else's server key")
		forged, original := startTLS("forged")
		Expect(original.Immutable).NotTo(BeNil())
		Expect(*original.Immutable).To(BeTrue())
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
		expectReplaced(forged, *impostor, true)

		// swapSecret stands in for someone who can delete and create Secrets: the operator's Secrets are
		// immutable, so changing one means recreating it, owner reference and all.
		swapSecret := func(original corev1.Secret, mutate func(*corev1.Secret)) corev1.Secret {
			GinkgoHelper()
			Expect(c.Delete(ctx, &original)).To(Succeed())
			swapped := corev1.Secret{
				Name:            original.Name,
				Namespace:       original.Namespace,
				OwnerReferences: original.OwnerReferences,
				Type:            original.Type,
				Immutable:       original.Immutable,
				Data:            maps.Clone(original.Data),
			}
			mutate(&swapped)
			Expect(c.Create(ctx, &swapped)).To(Succeed())
			return swapped
		}

		By("replacing its own Secret once the client CA no longer matches the spec")
		tampered, tamperedSecret := startTLS("tampered")
		expectReplaced(tampered, swapSecret(tamperedSecret, func(s *corev1.Secret) {
			s.Data[buildkit.SecretClientCAKey] = []byte(testClientCA())
		}), true)

		By("replacing its own Secret once the server key is unusable")
		keyless, keylessSecret := startTLS("keyless")
		expectReplaced(keyless, swapSecret(keylessSecret, func(s *corev1.Secret) {
			// Type kubernetes.io/tls requires the key to be present, but not non-empty.
			s.Data[corev1.TLSPrivateKeyKey] = []byte{}
		}), true)

		By("replacing a genuine copy of its Secret that is mutable")
		mutable, mutableSecret := startTLS("mutable")
		expectReplaced(mutable, swapSecret(mutableSecret, func(s *corev1.Secret) { s.Immutable = nil }), true)
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
			var secrets corev1.SecretList
			g.Expect(c.List(ctx, &secrets, client.InNamespace(tlsBadCANamespace), client.HasLabels{buildkit.TLSSecretLabel})).To(Succeed())
			g.Expect(secrets.Items).To(BeEmpty())
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
		var original corev1.Secret
		Eventually(func(g Gomega) { original = currentSecret(g, bk) }).Should(Succeed())
		Expect(c.Delete(ctx, &original)).To(Succeed())
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
		Eventually(func(g Gomega) { secret = currentSecret(g, bk) }).Should(Succeed())
		Expect(secret.Name).NotTo(Equal(original.Name))
		Expect(mountedSecret(pods.Items[0])).To(Equal(secret.Name))
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

	It("stops advertising a TLS pod that is terminating", func() {
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
		podKey := client.ObjectKeyFromObject(&pods.Items[0])

		// Envtest has no kubelet, so pods vanish on delete; a finalizer keeps this one terminating.
		const finalizer = "test.buildkit.seatgeek.io/hold"
		Eventually(func(g Gomega) {
			var pod corev1.Pod
			g.Expect(c.Get(ctx, podKey, &pod)).To(Succeed())
			pod.Finalizers = append(pod.Finalizers, finalizer)
			g.Expect(c.Update(ctx, &pod)).To(Succeed())
		}).Should(Succeed())
		DeferCleanup(func() {
			var pod corev1.Pod
			if err := c.Get(ctx, podKey, &pod); err == nil {
				pod.Finalizers = nil
				Expect(c.Update(ctx, &pod)).To(Succeed())
			}
		})
		markPodReady(podKey)
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).NotTo(BeEmpty())
		}).Should(Succeed())

		By("deleting the pod without touching its Secret")
		Expect(c.Delete(ctx, &pods.Items[0])).To(Succeed())
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed).Reason).To(Equal(api.ConditionReason("PodTerminating")))
			g.Expect(updated.Status.Endpoint).To(BeEmpty())
			g.Expect(updated.Status.ServerCA).To(BeEmpty())
		}).Should(Succeed())
	})

	It("gives a Buildkit recreated under the same name a Secret name never used before", func() {
		Expect(c.Create(ctx, &corev1.Namespace{Name: tlsRecreateNamespace})).To(Succeed())
		cleanupNamespace(tlsRecreateNamespace)
		DeferCleanup(func() {
			Expect(c.DeleteAllOf(ctx, &corev1.Secret{}, client.InNamespace(tlsRecreateNamespace))).To(Succeed())
		})

		template := &v1alpha1.BuildkitTemplate{
			Name:      "recreate-template",
			Namespace: tlsRecreateNamespace,
			Spec:      v1alpha1.BuildkitTemplateSpec{Port: 1234, HealthPort: 1235, RequireClientTLS: true},
		}
		Expect(c.Create(ctx, template)).To(Succeed())
		newBuildkit := func() *v1alpha1.Buildkit {
			return &v1alpha1.Buildkit{
				Name:      "reused",
				Namespace: tlsRecreateNamespace,
				Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
			}
		}

		first := newBuildkit()
		Expect(c.Create(ctx, first)).To(Succeed())
		var firstSecret corev1.Secret
		Eventually(func(g Gomega) { firstSecret = currentSecret(g, first) }).Should(Succeed())

		// Envtest has no garbage collector, so the first instance's Secret and pod outlive it, much as
		// a pod still terminating on a node would.
		Expect(c.Delete(ctx, first)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(first), &v1alpha1.Buildkit{}))).To(BeTrue())
		}).Should(Succeed())

		second := newBuildkit()
		Expect(c.Create(ctx, second)).To(Succeed())
		Eventually(func(g Gomega) {
			secret := currentSecret(g, second)
			g.Expect(secret.Name).NotTo(Equal(firstSecret.Name))
			g.Expect(string(secret.Data[buildkit.SecretClientCAKey])).To(Equal(second.Spec.ClientCA))

			var pods corev1.PodList
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsRecreateNamespace))).To(Succeed())
			mounts := make([]string, 0, len(pods.Items))
			for _, pod := range pods.Items {
				mounts = append(mounts, mountedSecret(pod))
			}
			g.Expect(mounts).To(ContainElement(secret.Name))
		}).Should(Succeed())
	})
})
