// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_test

import (
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

	It("refuses TLS secrets it doesn't own or whose client CA doesn't match", func() {
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

		expectRefused := func(bk *v1alpha1.Buildkit) {
			GinkgoHelper()
			Eventually(func(g Gomega) {
				var updated v1alpha1.Buildkit
				g.Expect(c.Get(ctx, client.ObjectKeyFromObject(bk), &updated)).To(Succeed())
				cond := updated.GetCondition(v1alpha1.TypeDeployed)
				g.Expect(cond.Status).NotTo(Equal(corev1.ConditionTrue))
				g.Expect(cond.Message).To(ContainSubstring("TLS secret for this Buildkit is unusable"))
				g.Expect(cond.Message).NotTo(ContainSubstring("not controlled"))
				g.Expect(updated.Status.ServerCA).To(BeEmpty())
			}).Should(Succeed())
		}

		By("refusing a pre-existing Secret with the instance's name that it doesn't own")
		Expect(c.Create(ctx, &corev1.Secret{
			Name:      "squatted-tls",
			Namespace: tlsSecretNamespace,
			Data:      map[string][]byte{corev1.TLSCertKey: []byte("x"), corev1.TLSPrivateKeyKey: []byte("x"), buildkit.SecretClientCAKey: []byte("x")},
		})).To(Succeed())
		squatted := &v1alpha1.Buildkit{
			Name:      "squatted",
			Namespace: tlsSecretNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, squatted)).To(Succeed())
		expectRefused(squatted)

		By("refusing its own Secret once the client CA no longer matches the spec")
		tampered := &v1alpha1.Buildkit{
			Name:      "tampered",
			Namespace: tlsSecretNamespace,
			Spec:      v1alpha1.BuildkitSpec{Template: template.Name, ClientCA: testClientCA()},
		}
		Expect(c.Create(ctx, tampered)).To(Succeed())
		var secret corev1.Secret
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, client.ObjectKey{Namespace: tlsSecretNamespace, Name: "tampered-tls"}, &secret)).To(Succeed())
		}).Should(Succeed())
		secret.Data[buildkit.SecretClientCAKey] = []byte(testClientCA())
		Expect(c.Update(ctx, &secret)).To(Succeed())
		Expect(c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(tlsSecretNamespace))).To(Succeed())
		expectRefused(tampered)

		var pods corev1.PodList
		Consistently(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(tlsSecretNamespace))).To(Succeed())
			g.Expect(pods.Items).To(BeEmpty())
		}, "2s").Should(Succeed())
	})
})
