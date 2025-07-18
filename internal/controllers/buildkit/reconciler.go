// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/reddit/achilles-sdk-api/api"
	"github.com/reddit/achilles-sdk/pkg/fsm"
	"github.com/reddit/achilles-sdk/pkg/fsm/types"
	"github.com/reddit/achilles-sdk/pkg/io"
	"github.com/reddit/achilles-sdk/pkg/logging"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/controlplane"
)

//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkits,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkits/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkits/finalizers,verbs=update
//+kubebuilder:rbac:resources=pods,verbs=get;list;watch;create;update;patch;delete

const controllerName = "Buildkit"

type state = types.State[*v1alpha1.Buildkit]

type reconciler struct {
	c      *io.ClientApplicator
	scheme *runtime.Scheme
	log    *zap.SugaredLogger
}

func (r *reconciler) runBuildkit() *state {
	return &state{
		Name:      "run-buildkit",
		Condition: conditionDeployed,
		Transition: func(ctx context.Context, obj *v1alpha1.Buildkit, out *types.OutputSet) (*state, types.Result) {
			log := r.log.With("name", obj.Name, "namespace", obj.Namespace)

			// Check if we already have any Buildkit pods
			managedPods, err := r.getExistingManagedPods(ctx, obj, log)
			if err != nil {
				return nil, types.ErrorResult(err)
			}

			// Ensure we have exactly one Buildkit pod running, creating or deleting as necessary
			pod, err := r.ensureExactlyOnePod(ctx, obj, managedPods, out, log)
			if err != nil {
				return nil, types.ErrorResult(err)
			}

			// Do we need to add or remove anything? If so, apply those changes now and requeue.
			if out.GetApplied().Len() > 0 || out.GetDeleted().Len() > 0 {
				return nil, types.Result{
					Done:                   true,
					RequeueAfterCompletion: true,
					RequeueMsg:             "Applying pod changes",
					Reason:                 "ApplyingChanges",
				}
			}

			// At this point, we have exactly one pod, so we can proceed to check its status.
			// We'll empty the endpoint field and only set it once we confirm the pod is running and healthy.
			obj.Status.Endpoint = ""

			// Are all containers running and healthy?
			if pod.Status.Phase == corev1.PodFailed {
				log.Warnw("Buildkit pod has failed", "pod", pod.Name, "reason", pod.Status.Reason, "message", pod.Status.Message)
				return nil, types.Result{
					Done: true,
					CustomStatusCondition: &types.ResultStatusCondition{
						Reason:  api.ReasonUnavailable,
						Status:  corev1.ConditionFalse,
						Message: fmt.Sprintf("Buildkit pod %s has failed: %s", pod.Name, cmp.Or(pod.Status.Message, pod.Status.Reason, "unknown failure")),
					},
				}
			}

			if pod.Status.Phase != corev1.PodRunning {
				log.Debugw("Buildkit pod is not yet running", "pod", pod.Name, "phase", pod.Status.Phase)
				return nil, types.RequeueResultWithReasonAndBackoff("Buildkit pod not running", "PodNotRunning")
			}

			for _, containerStatus := range pod.Status.ContainerStatuses {
				if !containerStatus.Ready {
					log.Debugw("Buildkit pod container not ready", "pod", pod.Name, "container", containerStatus.Name)
					return nil, types.RequeueResultWithReasonAndBackoff("Buildkit pod container not ready", "ContainerNotReady")
				}
			}

			// If we reach here, the pod is running and all containers are ready!
			obj.Status.Endpoint = fmt.Sprintf("tcp://%s", net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(pod.Spec.Containers[0].Ports[0].ContainerPort))))

			return nil, types.DoneResult()
		},
	}
}

// getExistingManagedPods retrieves all pods that are tracked as resources managed by the Buildkit instance.
func (r *reconciler) getExistingManagedPods(ctx context.Context, obj *v1alpha1.Buildkit, log *zap.SugaredLogger) ([]corev1.Pod, error) {
	existingPods := make([]corev1.Pod, 0, 1) // we expect at most one pod to be managed
	for _, ref := range obj.Status.ResourceRefs {
		if ref.Kind != "Pod" {
			continue
		}

		var pod corev1.Pod
		if err := r.c.Get(ctx, ref.ObjectKey(), &pod); err != nil {
			if apierrors.IsNotFound(err) {
				// Pod may have been deleted without us knowing. Log a warning and let Achilles clean up the reference later.
				log.Warnf("managed resource '%s' not found, an external actor may have deleted it", ref)
				continue
			}

			return nil, fmt.Errorf("failed to get managed pod '%s': %w", ref, err)
		}

		existingPods = append(existingPods, pod)
	}

	return existingPods, nil
}

// ensureExactlyOnePod ensures that there is exactly one Buildkit pod running.
// If no pods are found, it enqueues one for creation.
// If exactly one pod is found, it returns that pod.
// If multiple pods are found, it enqueues the unexpected extras for deletion and returns the first one.
// Note that we don't actually apply those changes here, we just update the OutputSet with the changes to be applied.
func (r *reconciler) ensureExactlyOnePod(ctx context.Context, obj *v1alpha1.Buildkit, managedPods []corev1.Pod, out *types.OutputSet, log *zap.SugaredLogger) (*corev1.Pod, error) {
	if len(managedPods) > 1 {
		log.Warnw("Multiple Buildkit pods found, deleting extras", "count", len(managedPods))
		for _, pod := range managedPods[1:] {
			out.Delete(&pod)
		}

		return &managedPods[0], nil
	}

	if len(managedPods) == 0 {
		// No pod running yet, so create one
		pod, err := NewBuilder(obj, r.c.Client).BuildPod(ctx)
		if err != nil {
			log.Errorw("Failed to generate Buildkit pod definition", "error", err)
			return nil, fmt.Errorf("failed to build Buildkit pod: %w", err)
		}

		log.Info("Starting Buildkit instance")
		out.Apply(pod)

		return pod, nil
	}

	return &managedPods[0], nil
}

func SetupController(
	ctx context.Context,
	cpCtx controlplane.Context,
	mgr ctrl.Manager,
	rl workqueue.TypedRateLimiter[reconcile.Request],
	c *io.ClientApplicator,
) error {
	_, log, err := logging.ControllerCtx(ctx, controllerName)
	if err != nil {
		return err
	}

	r := &reconciler{
		c:      c,
		scheme: mgr.GetScheme(),
		log:    log,
	}

	builder := fsm.NewBuilder(
		&v1alpha1.Buildkit{},
		r.runBuildkit(),
		mgr.GetScheme(),
	).Manages(
		corev1.SchemeGroupVersion.WithKind("Pod"),
	)

	return builder.Build()(mgr, log, rl, cpCtx.Metrics)
}
