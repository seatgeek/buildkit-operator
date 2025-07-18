// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_template_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/reddit/achilles-sdk-api/api"
	sdktest "github.com/reddit/achilles-sdk/pkg/test"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

var _ = Describe("BuildkitTemplate Reconciler", func() {
	var (
		namespace        string
		buildkitTemplate *v1alpha1.BuildkitTemplate
	)

	const (
		someTomlContent = `
[worker.oci]
  enabled = true
[worker.containerd]
  enabled = false`

		someOtherTomlContent = `
[worker.containerd]
  enabled = true
[worker.oci]
  enabled = false`
	)

	BeforeEach(func() {
		namespace = fmt.Sprintf("reconciler-test-%s", sdktest.GenerateRandomString(8))
		Expect(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())

		buildkitTemplate = &v1alpha1.BuildkitTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-template",
				Namespace: namespace,
			},
			Spec: v1alpha1.BuildkitTemplateSpec{
				PodTemplate: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "buildkit",
								Image: "moby/buildkit:latest",
							},
						},
					},
				},
				BuildkitdToml: "", // Start with empty TOML
			},
		}

		DeferCleanup(func() {
			Expect(c.DeleteAllOf(ctx, &v1alpha1.BuildkitTemplate{}, client.InNamespace(namespace))).To(Succeed())
			Expect(c.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
		})
	})

	It("should handle empty buildkitd.toml", func() {
		By("creating a BuildkitTemplate with empty buildkitd.toml")
		Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())

		By("verifying no ConfigMap is created")
		configMapKey := client.ObjectKey{Name: fmt.Sprintf("buildkit-%s-toml", buildkitTemplate.Name), Namespace: namespace}
		Consistently(func(g Gomega) {
			configMap := &corev1.ConfigMap{}
			err := c.Get(ctx, configMapKey, configMap)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "ConfigMap should not exist")
		}).Should(Succeed())

		By("verifying Ready condition is True")
		Eventually(func(g Gomega) {
			var updated v1alpha1.BuildkitTemplate
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkitTemplate), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(api.TypeReady).Status).To(Equal(corev1.ConditionTrue))
		}).Should(Succeed())
	})

	It("should create ConfigMap when buildkitd.toml is added", func() {
		By("creating BuildkitTemplate first")
		Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())

		By("updating BuildkitTemplate with buildkitd.toml content")
		buildkitTemplate.Spec.BuildkitdToml = someTomlContent
		Expect(c.Update(ctx, buildkitTemplate)).To(Succeed())

		By("verifying ConfigMap is created with correct content")
		configMapKey := client.ObjectKey{Name: fmt.Sprintf("buildkit-%s-toml", buildkitTemplate.Name), Namespace: namespace}
		Eventually(func(g Gomega) {
			configMap := &corev1.ConfigMap{}
			g.Expect(c.Get(ctx, configMapKey, configMap)).To(Succeed())
			g.Expect(configMap.Data).To(HaveKeyWithValue("buildkitd.toml", someTomlContent))
		}).Should(Succeed())

		By("verifying Ready condition remains True")
		Eventually(func(g Gomega) {
			var updated v1alpha1.BuildkitTemplate
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkitTemplate), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(api.TypeReady).Status).To(Equal(corev1.ConditionTrue))
		}).Should(Succeed())
	})

	It("should update ConfigMap when buildkitd.toml changes", func() {
		By("creating BuildkitTemplate with initial TOML content")
		buildkitTemplate.Spec.BuildkitdToml = someTomlContent
		Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())

		By("updating BuildkitTemplate with different buildkitd.toml content")
		buildkitTemplate.Spec.BuildkitdToml = someOtherTomlContent
		Expect(c.Update(ctx, buildkitTemplate)).To(Succeed())

		By("verifying ConfigMap data is updated")
		configMapKey := client.ObjectKey{Name: fmt.Sprintf("buildkit-%s-toml", buildkitTemplate.Name), Namespace: namespace}
		Eventually(func(g Gomega) {
			configMap := &corev1.ConfigMap{}
			g.Expect(c.Get(ctx, configMapKey, configMap)).To(Succeed())
			g.Expect(configMap.Data).To(HaveKeyWithValue("buildkitd.toml", someOtherTomlContent))
		}).Should(Succeed())

		By("verifying Ready condition remains True")
		Eventually(func(g Gomega) {
			var updated v1alpha1.BuildkitTemplate
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkitTemplate), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(api.TypeReady).Status).To(Equal(corev1.ConditionTrue))
		}).Should(Succeed())
	})

	It("should delete ConfigMap when buildkitd.toml is removed", func() {
		By("creating BuildkitTemplate with initial TOML content")
		buildkitTemplate.Spec.BuildkitdToml = someTomlContent
		Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())

		By("updating BuildkitTemplate back to empty buildkitd.toml")
		buildkitTemplate.Spec.BuildkitdToml = ""
		Expect(c.Update(ctx, buildkitTemplate)).To(Succeed())

		By("verifying ConfigMap is deleted")
		configMapKey := client.ObjectKey{Name: fmt.Sprintf("buildkit-%s-toml", buildkitTemplate.Name), Namespace: namespace}
		Eventually(func(g Gomega) {
			configMap := &corev1.ConfigMap{}
			err := c.Get(ctx, configMapKey, configMap)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "ConfigMap should be deleted")
		}).Should(Succeed())

		By("verifying Ready condition remains True")
		Eventually(func(g Gomega) {
			var updated v1alpha1.BuildkitTemplate
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkitTemplate), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(api.TypeReady).Status).To(Equal(corev1.ConditionTrue))
		}).Should(Succeed())
	})
})
