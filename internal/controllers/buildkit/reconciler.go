// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/reddit/achilles-sdk/pkg/fsm"
	fsmhandler "github.com/reddit/achilles-sdk/pkg/fsm/handler"
	"github.com/reddit/achilles-sdk/pkg/fsm/types"
	"github.com/reddit/achilles-sdk/pkg/io"
	"github.com/reddit/achilles-sdk/pkg/logging"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/controlplane"
	"github.com/seatgeek/buildkit-operator/internal/tlsutil"
)

//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkits,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkits/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkits/finalizers,verbs=update
//+kubebuilder:rbac:resources=pods,verbs=get;list;watch;create;update;patch;delete

const controllerName = "Buildkit"

const reasonApplyingChanges = "ApplyingChanges"

type state = types.State[*v1alpha1.Buildkit]

type reconciler struct {
	c      *io.ClientApplicator
	scheme *runtime.Scheme
	log    *zap.SugaredLogger
	// healthProxyImage runs the gen-cert and health-proxy containers of TLS pods.
	healthProxyImage string
	now              func() time.Time
}

func (r *reconciler) runBuildkit() *state {
	return &state{
		Name:      "run-buildkit",
		Condition: conditionDeployed,
		Transition: func(ctx context.Context, obj *v1alpha1.Buildkit, out *types.OutputSet) (*state, types.Result) {
			log := r.log.With("name", obj.Name, "namespace", obj.Namespace)

			template, err := r.getTemplate(ctx, obj)
			if err != nil {
				return nil, types.ErrorResult(err)
			}
			requireTLS := template != nil && template.Spec.RequireClientTLS

			// Check if we already have any Buildkit pods
			managedPods, err := r.getExistingManagedPods(ctx, obj, log)
			if err != nil {
				return nil, types.ErrorResult(err)
			}

			if requireTLS {
				if stop, err := r.prepareTLS(ctx, obj, managedPods, out, log); err != nil {
					return nil, types.ErrorResult(err)
				} else if stop != nil {
					return nil, *stop
				}
			} else if obj.Status.ServerCA != "" && len(podsWithoutTLS(managedPods)) == len(managedPods) {
				// TLS was turned off and no TLS pod remains. Withdraw the TLS endpoint and its pinned
				// certificate; the plaintext pod's endpoint is published once it is ready.
				if err := r.clearConnectionStatus(ctx, obj); err != nil {
					return nil, types.ErrorResult(err)
				}
			}

			// The pod may have vanished before this controller saw it terminating; stop advertising it.
			if len(managedPods) == 0 {
				if err := r.clearConnectionStatus(ctx, obj); err != nil {
					return nil, types.ErrorResult(err)
				}
			}

			// Ensure we have exactly one Buildkit pod running, creating or deleting as necessary
			pod, err := r.ensureExactlyOnePod(ctx, obj, managedPods, requireTLS, out, log)
			if err != nil {
				return nil, types.ErrorResult(err)
			}

			// Do we need to add or remove anything? If so, apply those changes now and requeue.
			if out.GetApplied().Len() > 0 || out.GetDeleted().Len() > 0 {
				return nil, types.Result{
					Done:                   true,
					RequeueAfterCompletion: true,
					RequeueMsg:             "Applying pod changes",
					Reason:                 reasonApplyingChanges,
				}
			}

			// Whether the running pod serves TLS, which can differ from what the template now requires
			// (requireClientTLS turned off leaves a TLS pod running until it is replaced).
			podTLS := len(podsWithoutTLS([]corev1.Pod{*pod})) == 0

			// A terminating TLS pod may still look ready, or be held by a finalizer; stop advertising it
			// rather than let clients connect to an instance on its way out.
			if podTLS && pod.DeletionTimestamp != nil {
				if err := r.clearConnectionStatus(ctx, obj); err != nil {
					return nil, types.ErrorResult(err)
				}
				return nil, types.RequeueResultWithReasonAndBackoff("Buildkit pod terminating", "PodTerminating")
			}

			// At this point, we have exactly one pod, so we can proceed to check its status. Until it is
			// running and healthy, nothing is advertised.
			notReady := func(result types.Result) (*state, types.Result) {
				if err := r.clearConnectionStatus(ctx, obj); err != nil {
					return nil, types.ErrorResult(err)
				}
				return nil, result
			}

			// Are all containers running and healthy?
			if pod.Status.Phase == corev1.PodFailed {
				log.Warnw("Buildkit pod has failed", "pod", pod.Name, "reason", pod.Status.Reason, "message", pod.Status.Message)
				return notReady(types.Result{
					Done: true,
					CustomStatusCondition: &types.ResultStatusCondition{
						Reason:  "PodFailed",
						Status:  corev1.ConditionFalse,
						Message: fmt.Sprintf("Buildkit pod %s has failed: %s", pod.Name, cmp.Or(pod.Status.Message, pod.Status.Reason, "unknown failure")),
					},
				})
			}

			if pod.Status.Phase != corev1.PodRunning {
				log.Debugw("Buildkit pod is not yet running", "pod", pod.Name, "phase", pod.Status.Phase)
				return notReady(types.RequeueResultWithReasonAndBackoff("Buildkit pod not running", "PodNotRunning"))
			}

			for _, containerStatus := range pod.Status.ContainerStatuses {
				if !containerStatus.Ready {
					log.Debugw("Buildkit pod container not ready", "pod", pod.Name, "container", containerStatus.Name)
					return notReady(types.RequeueResultWithReasonAndBackoff("Buildkit pod container not ready", "ContainerNotReady"))
				}
			}

			// If we reach here, the pod is running and all containers are ready!
			if len(pod.Spec.Containers) == 0 || len(pod.Spec.Containers[0].Ports) == 0 {
				log.Errorw("Buildkit pod does not have containers with ports defined", "pod", pod.Name)
				return nil, types.ErrorResult(fmt.Errorf("buildkit pod %s does not have containers with ports defined", pod.Name))
			}

			if podTLS {
				cert, err := podServerCert(pod, obj.Name)
				if err != nil {
					log.Errorw("Buildkit pod reported no usable server certificate", "pod", pod.Name, "error", err)
					return notReady(types.ErrorResult(fmt.Errorf("buildkit pod %s: %w", pod.Name, err)))
				}
				obj.Status.ServerCA = cert
				obj.Status.ServerName = obj.Name
			}

			obj.Status.Endpoint = fmt.Sprintf("tcp://%s", net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(pod.Spec.Containers[0].Ports[0].ContainerPort))))

			if podTLS {
				return nil, r.recheckAtClientCAExpiry(obj)
			}
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
func (r *reconciler) ensureExactlyOnePod(ctx context.Context, obj *v1alpha1.Buildkit, managedPods []corev1.Pod, requireTLS bool, out *types.OutputSet, log *zap.SugaredLogger) (*corev1.Pod, error) {
	if len(managedPods) > 1 {
		log.Warnw("Multiple Buildkit pods found, deleting extras", "count", len(managedPods))
		for _, pod := range managedPods[1:] {
			out.Delete(&pod)
		}

		return &managedPods[0], nil
	}

	if len(managedPods) == 0 {
		// No pod running yet, so create one
		builder := NewBuilder(obj, r.c.Client)
		if requireTLS {
			builder.WithTLS(r.healthProxyImage)
		}
		pod, err := builder.BuildPod(ctx)
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

// clearConnectionStatus removes the endpoint and TLS details so clients stop using an instance that
// has been stopped. The FSM writes status as a JSON merge patch of omitempty fields, which cannot
// clear a field, so this patches them to null directly.
func (r *reconciler) clearConnectionStatus(ctx context.Context, obj *v1alpha1.Buildkit) error {
	if obj.Status.Endpoint == "" && obj.Status.ServerCA == "" && obj.Status.ServerName == "" {
		return nil
	}

	p := []byte(`{"status":{"endpoint":null,"serverCA":null,"serverName":null}}`)
	if err := r.c.Status().Patch(ctx, obj, client.RawPatch(k8stypes.MergePatchType, p)); err != nil {
		return fmt.Errorf("clearing connection status: %w", err)
	}

	obj.Status.Endpoint = ""
	obj.Status.ServerCA = ""
	obj.Status.ServerName = ""
	return nil
}

// prepareTLS checks a Buildkit whose template requires client TLS before its pod is created or
// checked. It returns a result when the reconcile must stop here.
func (r *reconciler) prepareTLS(ctx context.Context, obj *v1alpha1.Buildkit, managedPods []corev1.Pod, out *types.OutputSet, log *zap.SugaredLogger) (*types.Result, error) {
	// Checked before anything is deleted: an operator that can't start TLS pods must leave running
	// ones alone.
	if r.healthProxyImage == "" {
		return &types.Result{
			Done: true,
			CustomStatusCondition: &types.ResultStatusCondition{
				Reason:  ReasonTLSNotConfigured,
				Status:  corev1.ConditionFalse,
				Message: fmt.Sprintf("BuildkitTemplate %s requires client TLS but the operator has no --health-proxy-image", obj.Spec.Template),
			},
		}, nil
	}

	// A pod started before the template required TLS listens in plaintext and can't be upgraded in
	// place, so remove it. A TLS pod replaces it if the Buildkit has a client CA.
	if plaintext := podsWithoutTLS(managedPods); len(plaintext) > 0 {
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, err
		}
		if deleting := notYetDeleting(plaintext); len(deleting) > 0 {
			log.Infow("Removing plaintext Buildkit pods because their template requires client TLS", "count", len(deleting))
			if err := r.forceDeletePods(ctx, deleting, out); err != nil {
				return nil, err
			}
			return &types.Result{Done: true, RequeueAfterCompletion: true, RequeueMsg: "Removing plaintext pod", Reason: reasonApplyingChanges}, nil
		}
		result := types.RequeueResultWithReasonAndBackoff("Buildkit pod terminating", "PodTerminating")
		return &result, nil
	}

	if obj.Spec.ClientCA == "" {
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, err
		}
		return &types.Result{
			Done: true,
			CustomStatusCondition: &types.ResultStatusCondition{
				Reason:  ReasonClientCARequired,
				Status:  corev1.ConditionFalse,
				Message: fmt.Sprintf("BuildkitTemplate %s requires client TLS but spec.clientCA is empty", obj.Spec.Template),
			},
		}, nil
	}

	// The webhook enforces this too; checking here keeps the controller safe without it, and catches a
	// client CA that has expired since (no client could connect with it).
	if problem := clientCAProblem(obj.Spec.ClientCA, r.now()); problem != "" {
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, err
		}
		return &types.Result{
			Done: true,
			CustomStatusCondition: &types.ResultStatusCondition{
				Reason:  ReasonClientCAInvalid,
				Status:  corev1.ConditionFalse,
				Message: "spec.clientCA " + problem,
			},
		}, nil
	}

	return nil, nil //nolint:nilnil // no result: the reconcile continues
}

// notYetDeleting returns the pods forceDeletePods still has work to do on: those not being deleted,
// and those being deleted with a longer grace period (e.g. a normal delete before the template
// required TLS), which may keep serving for the rest of it.
func notYetDeleting(pods []corev1.Pod) []corev1.Pod {
	var out []corev1.Pod
	for _, pod := range pods {
		if pod.DeletionTimestamp == nil || pod.DeletionGracePeriodSeconds == nil || *pod.DeletionGracePeriodSeconds > forceDeleteGracePeriodSeconds {
			out = append(out, pod)
		}
	}
	return out
}

const forceDeleteGracePeriodSeconds = 1

// forceDeletePods deletes pods with a one-second grace period. A pod terminating on the template's
// grace period would keep serving (plaintext, or with a certificate no longer in status) while its
// pre-stop script waits, and the controller would keep treating it as the live instance. A zero grace
// period would instead remove the pod object before kubelet has stopped the container.
func (r *reconciler) forceDeletePods(ctx context.Context, pods []corev1.Pod, out *types.OutputSet) error {
	for i := range pods {
		if err := r.c.Delete(ctx, &pods[i], client.GracePeriodSeconds(forceDeleteGracePeriodSeconds)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting pod %s: %w", pods[i].Name, err)
		}
		out.Delete(&pods[i])
	}
	return nil
}

// clientCAProblem describes why the client CA can't authenticate clients now, or returns "". It
// mirrors the webhook's checks so the controller is safe without it.
func clientCAProblem(clientCA string, now time.Time) string {
	ca, err := tlsutil.ParseCACertPEM(clientCA)
	if err != nil {
		return "is not a single PEM CA certificate: " + err.Error()
	}
	if now.After(ca.NotAfter) {
		return "expired at " + ca.NotAfter.UTC().Format(time.RFC3339) + "; create a new Buildkit with a current CA"
	}
	if ca.NotBefore.After(now.Add(tlsutil.ClockSkew)) {
		return "is not valid until " + ca.NotBefore.UTC().Format(time.RFC3339)
	}
	return ""
}

// recheckAtClientCAExpiry keeps the instance ready but reconciles again just after the client CA
// expires. Nothing else would: the pod stays healthy, so no event fires, yet no client can connect.
// Achilles requeues only when RequeueMsg is set, and counts any requeue as an unsuccessful reconcile
// in its processing-duration metric.
func (r *reconciler) recheckAtClientCAExpiry(obj *v1alpha1.Buildkit) types.Result {
	ca, err := tlsutil.ParseCACertPEM(obj.Spec.ClientCA)
	if err != nil {
		return types.ErrorResult(fmt.Errorf("parsing client CA: %w", err))
	}
	// clientCAProblem treats NotAfter itself as still valid.
	return types.Result{
		Done:         true,
		RequeueMsg:   "Re-checking at client CA expiry",
		RequeueAfter: ca.NotAfter.Sub(r.now()) + time.Second,
	}
}

// podServerCert returns the server certificate the pod's gen-cert container minted, which it reports in
// its termination message. Only kubelet writes container statuses.
func podServerCert(pod *corev1.Pod, serverName string) (string, error) {
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name != genCertContainerName {
			continue
		}
		terminated := status.State.Terminated
		if terminated == nil || terminated.ExitCode != 0 {
			return "", fmt.Errorf("%s has not completed successfully", genCertContainerName)
		}
		if _, err := tlsutil.ParseServerCertPEM([]byte(terminated.Message), serverName); err != nil {
			return "", fmt.Errorf("%s reported an unusable server certificate: %w", genCertContainerName, err)
		}
		return terminated.Message, nil
	}
	return "", fmt.Errorf("pod has no %s container status", genCertContainerName)
}

// getTemplate returns the Buildkit's template, or nil if it doesn't exist; BuildPod fails on a missing
// template before any pod could be created.
func (r *reconciler) getTemplate(ctx context.Context, obj *v1alpha1.Buildkit) (*v1alpha1.BuildkitTemplate, error) {
	var template v1alpha1.BuildkitTemplate
	if err := r.c.Get(ctx, client.ObjectKey{Namespace: obj.Namespace, Name: obj.Spec.Template}, &template); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil //nolint:nilnil // a missing template is not an error here
		}
		return nil, fmt.Errorf("getting BuildkitTemplate %s: %w", obj.Spec.Template, err)
	}
	return &template, nil
}

// podsWithoutTLS returns the pods whose buildkitd doesn't require client certificates. It checks the
// container args, which only the operator sets, rather than metadata a Buildkit's creator can supply.
func podsWithoutTLS(pods []corev1.Pod) []corev1.Pod {
	var out []corev1.Pod
	for _, pod := range pods {
		if len(pod.Spec.Containers) == 0 || !slices.Contains(pod.Spec.Containers[0].Args, tlsCACertFlag) {
			out = append(out, pod)
		}
	}
	return out
}

// buildkitsForTLSTemplate enqueues the Buildkits using a template that requires client TLS.
func (r *reconciler) buildkitsForTLSTemplate(ctx context.Context, obj client.Object) []reconcile.Request {
	template, ok := obj.(*v1alpha1.BuildkitTemplate)
	if !ok || !template.Spec.RequireClientTLS {
		return nil
	}

	var buildkits v1alpha1.BuildkitList
	if err := r.c.List(ctx, &buildkits, client.InNamespace(template.Namespace)); err != nil {
		r.log.Errorw("Failed to list Buildkits for template", "template", template.Name, "namespace", template.Namespace, "error", err)
		return nil
	}

	var requests []reconcile.Request
	for _, bk := range buildkits.Items {
		if bk.Spec.Template == template.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bk)})
		}
	}
	return requests
}

func SetupController(
	ctx context.Context,
	cpCtx controlplane.Context,
	mgr ctrl.Manager,
	rl workqueue.TypedRateLimiter[reconcile.Request],
	c *io.ClientApplicator,
	healthProxyImage string,
) error {
	_, log, err := logging.ControllerCtx(ctx, controllerName)
	if err != nil {
		return err
	}

	r := &reconciler{
		c:                c,
		scheme:           mgr.GetScheme(),
		log:              log,
		healthProxyImage: healthProxyImage,
		now:              time.Now,
	}

	builder := fsm.NewBuilder(
		&v1alpha1.Buildkit{},
		r.runBuildkit(),
		mgr.GetScheme(),
	).Manages(
		corev1.SchemeGroupVersion.WithKind("Pod"),
	).Watches(
		// Requiring TLS must stop existing plaintext instances promptly, not at their next pod event.
		&v1alpha1.BuildkitTemplate{},
		handler.EnqueueRequestsFromMapFunc(r.buildkitsForTLSTemplate),
		fsmhandler.TriggerTypeRelative,
	)

	return builder.Build()(mgr, log, rl, cpCtx.Metrics)
}
