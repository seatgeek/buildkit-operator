// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/reddit/achilles-sdk-api/api"
	sdktest "github.com/reddit/achilles-sdk/pkg/test"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	. "github.com/seatgeek/buildkit-operator/internal/test/matchers"
)

var _ = Describe("Buildkit Reconciler", func() {
	var (
		namespace        string
		buildkitTemplate *v1alpha1.BuildkitTemplate
		buildkit         *v1alpha1.Buildkit
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
				Port: 1234,
			},
		}
		Expect(c.Create(ctx, buildkitTemplate)).To(Succeed())

		buildkit = &v1alpha1.Buildkit{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-buildkit",
				Namespace: namespace,
			},
			Spec: v1alpha1.BuildkitSpec{
				Template: buildkitTemplate.Name,
			},
		}

		DeferCleanup(func() {
			Expect(c.DeleteAllOf(ctx, &v1alpha1.Buildkit{}, client.InNamespace(namespace))).To(Succeed())
			Expect(c.DeleteAllOf(ctx, &v1alpha1.BuildkitTemplate{}, client.InNamespace(namespace))).To(Succeed())
			Expect(c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(namespace))).To(Succeed())
			Expect(c.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
		})
	})

	It("should reconcile Buildkit and create a pod", func() {
		By("creating a Buildkit resource")
		Expect(c.Create(ctx, buildkit)).To(Succeed())

		By("verifying a pod is created")
		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(namespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())

		pod := &pods.Items[0]
		podKey := client.ObjectKeyFromObject(pod)

		By("simulating pod transition to running with waiting containers")
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, podKey, pod)).To(Succeed())
			pod.Status.Phase = corev1.PodRunning
			pod.Status.PodIP = "10.0.0.1"
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name:  "buildkit",
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "ContainerCreating",
						},
					},
				},
			}
			g.Expect(c.Status().Update(ctx, pod)).To(Succeed())
		}).Should(Succeed())

		By("verifying buildkit is not yet ready")
		Consistently(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkit), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed).Status).To(Equal(corev1.ConditionFalse))
			g.Expect(updated.GetCondition(api.TypeReady).Status).To(Equal(corev1.ConditionFalse))
			g.Expect(updated.Status.Endpoint).To(BeEmpty())
		}).Should(Succeed())

		By("simulating containers becoming ready")
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, podKey, pod)).To(Succeed())
			pod.Status.ContainerStatuses[0].Ready = true
			pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{},
			}
			g.Expect(c.Status().Update(ctx, pod)).To(Succeed())
		}).Should(Succeed())

		By("verifying endpoint is set with correct format")
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkit), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).To(Equal("tcp://10.0.0.1:1234"))
		}).Should(Succeed())

		By("verifying Deployed condition is True and Buildkit is Ready")
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkit), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed).Status).To(Equal(corev1.ConditionTrue))
			g.Expect(updated.GetCondition(api.TypeReady).Status).To(Equal(corev1.ConditionTrue))
		}).Should(Succeed())
	})

	It("should handle pod failure by setting condition to false", func() {
		By("creating a Buildkit resource")
		Expect(c.Create(ctx, buildkit)).To(Succeed())

		By("waiting for pod to be created")
		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(namespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())

		pod := &pods.Items[0]
		podKey := client.ObjectKeyFromObject(pod)

		By("simulating pod failure")
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, podKey, pod)).To(Succeed())
			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "ContainerCannotRun"
			pod.Status.Message = "Container failed to start"
			g.Expect(c.Status().Update(ctx, pod)).To(Succeed())
		}).Should(Succeed())

		By("verifying endpoint is unset and Deployed condition is False")
		Eventually(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkit), &updated)).To(Succeed())
			g.Expect(updated.Status.Endpoint).To(BeEmpty())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed)).To(MatchCondition(api.Condition{
				Status: corev1.ConditionFalse,
				Reason: "PodFailed",
			}))
		}).Should(Succeed())
	})

	It("should recreate pod when externally deleted", func() {
		By("creating a Buildkit resource and waiting for ready pod")
		Expect(c.Create(ctx, buildkit)).To(Succeed())

		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(namespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())

		By("recording the original pod name")
		originalPod := &pods.Items[0]
		originalPodName := originalPod.Name

		By("simulating external deletion of the managed pod")
		Expect(c.Delete(ctx, originalPod)).To(Succeed())

		By("verifying a new pod is created with a different name")
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(namespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Name).NotTo(Equal(originalPodName))
		}).Should(Succeed())
	})

	It("should not modify existing pods when BuildkitTemplate changes", func() {
		By("creating a Buildkit resource")
		Expect(c.Create(ctx, buildkit)).To(Succeed())

		By("waiting for pod to be created")
		var pods corev1.PodList
		Eventually(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(namespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
		}).Should(Succeed())

		pod := &pods.Items[0]
		podKey := client.ObjectKeyFromObject(pod)

		By("simulating pod transition to running and ready")
		Eventually(func(g Gomega) {
			g.Expect(c.Get(ctx, podKey, pod)).To(Succeed())
			pod.Status.Phase = corev1.PodRunning
			pod.Status.PodIP = "10.0.0.1"
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name:  "buildkit",
					Ready: true,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			}
			g.Expect(c.Status().Update(ctx, pod)).To(Succeed())
		}).Should(Succeed())

		By("recording the original pod configuration")
		var originalPod corev1.Pod
		Expect(c.Get(ctx, podKey, &originalPod)).To(Succeed())
		originalName := originalPod.Name
		originalUID := originalPod.UID
		originalGeneration := originalPod.Generation
		originalResourceVersion := originalPod.ResourceVersion

		By("updating the BuildkitTemplate with different configuration")
		Eventually(func(g Gomega) {
			var updatedTemplate v1alpha1.BuildkitTemplate
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkitTemplate), &updatedTemplate)).To(Succeed())
			updatedTemplate.Spec.Port = 5678 // Change port from 1234 to 5678
			g.Expect(c.Update(ctx, &updatedTemplate)).To(Succeed())
		}).Should(Succeed())

		By("waiting for the BuildkitTemplate to reconcile")
		Eventually(func(g Gomega) {
			var updated v1alpha1.BuildkitTemplate
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkitTemplate), &updated)).To(Succeed())
			g.Expect(updated.Spec.Port).To(Equal(int32(5678)))
		}).Should(Succeed())

		By("verifying only one pod still exists")
		Consistently(func(g Gomega) {
			g.Expect(c.List(ctx, &pods, client.InNamespace(namespace))).To(Succeed())
			g.Expect(pods.Items).To(HaveLen(1))
			g.Expect(pods.Items[0].Name).To(Equal(originalName))
		}).Should(Succeed())

		By("verifying the existing pod remains unchanged")
		Consistently(func(g Gomega) {
			var currentPod corev1.Pod
			g.Expect(c.Get(ctx, podKey, &currentPod)).To(Succeed())
			g.Expect(currentPod.Name).To(Equal(originalName))
			g.Expect(currentPod.UID).To(Equal(originalUID))
			g.Expect(currentPod.ResourceVersion).To(Equal(originalResourceVersion))
			g.Expect(currentPod.Generation).To(Equal(originalGeneration))
			g.Expect(currentPod.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(1234))) // Still using original port
		}).Should(Succeed())

		By("verifying the Buildkit resource remains ready")
		Consistently(func(g Gomega) {
			var updated v1alpha1.Buildkit
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(buildkit), &updated)).To(Succeed())
			g.Expect(updated.GetCondition(v1alpha1.TypeDeployed).Status).To(Equal(corev1.ConditionTrue))
			g.Expect(updated.Status.Endpoint).To(Equal("tcp://10.0.0.1:1234")) // Still using original port
		}).Should(Succeed())
	})
})
