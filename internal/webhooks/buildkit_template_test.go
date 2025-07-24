// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package webhooks

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sdktest "github.com/reddit/achilles-sdk/pkg/test"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

var _ = Describe("BuildkitTemplateValidator", func() {
	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("webhook-test-%s", sdktest.GenerateRandomString(8))
		Expect(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())

		DeferCleanup(func() {
			Expect(c.DeleteAllOf(ctx, &v1alpha1.BuildkitTemplate{}, client.InNamespace(namespace))).To(Succeed())
			Expect(c.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
		})
	})

	Context("When creating a new BuildkitTemplate resource", func() {
		It("should reject invalid TOML syntax", func() {
			buildkitTemplate := &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit-template",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					BuildkitdToml: `
[[[invalid toml
missing closing bracket`,
				},
			}

			Expect(c.Create(ctx, buildkitTemplate)).To(MatchError(ContainSubstring("toml: error:")))
		})

		It("should accept valid TOML syntax", func() {
			buildkitTemplate := &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit-template",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					BuildkitdToml: `
[worker.oci]
  enabled = true

[worker.containerd]
  enabled = false`,
				},
			}

			Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())
		})

		It("should accept empty TOML", func() {
			buildkitTemplate := &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit-template",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					BuildkitdToml: "",
				},
			}

			Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())
		})
	})

	Context("When updating a BuildkitTemplate resource", func() {
		var existingTemplate *v1alpha1.BuildkitTemplate

		BeforeEach(func() {
			existingTemplate = &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-template",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					BuildkitdToml: `
[worker.oci]
enabled = true`,
				},
			}
			Expect(c.Create(ctx, existingTemplate)).To(Succeed())
		})

		It("should reject updates with invalid TOML syntax", func() {
			existingTemplate.Spec.BuildkitdToml = `
[[[invalid toml
missing closing bracket`

			Expect(c.Update(ctx, existingTemplate)).To(MatchError(ContainSubstring("toml: error:")))
		})

		It("should accept updates with valid TOML syntax", func() {
			existingTemplate.Spec.BuildkitdToml = `
[worker.containerd]
enabled = true`

			Expect(c.Update(ctx, existingTemplate)).To(Succeed())
		})
	})
})

var _ = Describe("BuildkitTemplateDefaulter", func() {
	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("webhook-test-%s", sdktest.GenerateRandomString(8))
		Expect(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())

		DeferCleanup(func() {
			Expect(c.DeleteAllOf(ctx, &v1alpha1.BuildkitTemplate{}, client.InNamespace(namespace))).To(Succeed())
			Expect(c.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
		})
	})

	Context("When creating a BuildkitTemplate with default values", func() {
		It("should default missing fields", func() {
			buildkitTemplate := &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit-template",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitTemplateSpec{},
			}

			Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())

			var created v1alpha1.BuildkitTemplate
			Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkitTemplate), &created)).To(Succeed())

			Expect(created.Spec.Port).To(Equal(int32(1234)))
			Expect(created.Spec.Image).To(Equal("moby/buildkit:latest"))
			Expect(*created.Spec.Lifecycle.TerminationGracePeriodSeconds).To(Equal(int64(900)))
		})

		It("should not override explicitly set values", func() {
			customPort := int32(8080)
			customTerminationGracePeriod := int64(60)

			buildkitTemplate := &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit-template",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					Port:  customPort,
					Image: "moby/buildkit:v0.23.0",
					Lifecycle: v1alpha1.BuildkitTemplatePodLifecycle{
						TerminationGracePeriodSeconds: &customTerminationGracePeriod,
					},
				},
			}

			Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())

			var created v1alpha1.BuildkitTemplate
			Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkitTemplate), &created)).To(Succeed())
			Expect(created.Spec.Port).To(Equal(customPort))
			Expect(created.Spec.Image).To(Equal("moby/buildkit:v0.23.0"))
			Expect(*created.Spec.Lifecycle.TerminationGracePeriodSeconds).To(Equal(customTerminationGracePeriod))
		})
	})
})
