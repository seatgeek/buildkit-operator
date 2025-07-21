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

var _ = Describe("BuildkitValidator", func() {
	const someExistingTemplateName = "existing-template"

	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("webhook-test-%s", sdktest.GenerateRandomString(8))
		Expect(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())

		someExistingTemplate := &v1alpha1.BuildkitTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      someExistingTemplateName,
				Namespace: namespace,
			},
			Spec: v1alpha1.BuildkitTemplateSpec{
				PodTemplate: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{},
					},
				},
				BuildkitdToml: "",
			},
		}
		Expect(c.Create(ctx, someExistingTemplate)).To(Succeed())

		DeferCleanup(func() {
			Expect(c.DeleteAllOf(ctx, &v1alpha1.Buildkit{}, client.InNamespace(namespace))).To(Succeed())
			Expect(c.DeleteAllOf(ctx, &v1alpha1.BuildkitTemplate{}, client.InNamespace(namespace))).To(Succeed())
			Expect(c.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
		})
	})

	Context("When creating a new Buildkit resource", func() {
		It("should require the spec.template field to be set to a non-empty value", func() {
			buildkit := &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit",
					Namespace: "default",
				},
				Spec: v1alpha1.BuildkitSpec{
					// Template is intentionally left empty to test validation
				},
			}

			Expect(c.Create(ctx, buildkit)).To(MatchError(ContainSubstring("spec.template")))
		})

		It("should require the spec.template field to reference an existing BuildkitTemplate", func() {
			buildkit := &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: "non-existent-template",
				},
			}

			Expect(c.Create(ctx, buildkit)).To(MatchError(ContainSubstring("Not found: \"non-existent-template\"")))
		})

		It("should allow creation with a valid template reference", func() {
			buildkit := &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: someExistingTemplateName,
				},
			}

			Expect(c.Create(ctx, buildkit)).To(Succeed())
		})
	})

	Context("When RequireOwner validation is involved", func() {
		const templateWithRequireOwnerName = "template-with-require-owner"
		const templateWithoutRequireOwnerName = "template-without-require-owner"

		BeforeEach(func() {
			templateWithRequireOwner := &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      templateWithRequireOwnerName,
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					PodTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{},
						},
					},
					BuildkitdToml: "",
					RequireOwner:  true,
				},
			}
			Expect(c.Create(ctx, templateWithRequireOwner)).To(Succeed())

			templateWithoutRequireOwner := &v1alpha1.BuildkitTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      templateWithoutRequireOwnerName,
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitTemplateSpec{
					PodTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{},
						},
					},
					BuildkitdToml: "",
					RequireOwner:  false,
				},
			}
			Expect(c.Create(ctx, templateWithoutRequireOwner)).To(Succeed())
		})

		It("should allow creation when RequireOwner=false and no owner references", func() {
			buildkit := &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit-no-owner-not-required",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: templateWithoutRequireOwnerName,
				},
			}

			Expect(c.Create(ctx, buildkit)).To(Succeed())
		})

		It("should allow creation when RequireOwner=true and owner references are present", func() {
			buildkit := &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit-with-owner",
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Pod",
							Name:       "some-owner",
							UID:        "123456",
						},
					},
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: templateWithRequireOwnerName,
				},
			}

			Expect(c.Create(ctx, buildkit)).To(Succeed())
		})

		It("should reject creation when RequireOwner=true but no owner references are present", func() {
			buildkit := &v1alpha1.Buildkit{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-buildkit-no-owner",
					Namespace: namespace,
				},
				Spec: v1alpha1.BuildkitSpec{
					Template: templateWithRequireOwnerName,
				},
			}

			Expect(c.Create(ctx, buildkit)).To(MatchError(ContainSubstring("requires owner references but none are present")))
		})
	})
})
