// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"cmp"
	"context"
	"errors"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

// TLSSecretLabel marks Secrets holding a Buildkit's TLS material.
const TLSSecretLabel = "buildkit.seatgeek.io/tls"

// serverCertValidity bounds the server certificate's lifetime. Buildkit instances are meant to be
// short-lived; one outliving this needs recreating.
const serverCertValidity = 30 * 24 * time.Hour

// TLSConfig configures how the controller provisions mutual TLS for templates that require it.
type TLSConfig struct {
	// SecretClient reads and creates TLS Secrets. It must not be backed by the manager's cache, so the
	// operator never lists or caches Secrets.
	SecretClient client.Client
	// Namespaces are the namespaces where the operator has RBAC to manage TLS Secrets.
	Namespaces []string
	// HealthProxyImage runs the health-only sidecar; normally the operator's own image.
	HealthProxyImage string
}

type reconciler struct {
	c      *io.ClientApplicator
	scheme *runtime.Scheme
	log    *zap.SugaredLogger
	tls    TLSConfig
}

func (r *reconciler) runBuildkit() *state {
	return &state{
		Name:      "run-buildkit",
		Condition: conditionDeployed,
		Transition: func(ctx context.Context, obj *v1alpha1.Buildkit, out *types.OutputSet) (*state, types.Result) {
			log := r.log.With("name", obj.Name, "namespace", obj.Namespace)

			requireTLS, err := r.templateRequiresTLS(ctx, obj)
			if err != nil {
				return nil, types.ErrorResult(err)
			}

			// Check if we already have any Buildkit pods
			managedPods, err := r.getExistingManagedPods(ctx, obj, log)
			if err != nil {
				return nil, types.ErrorResult(err)
			}

			var tlsOpts *TLSOptions
			if requireTLS {
				var stop *types.Result
				if tlsOpts, stop, err = r.prepareTLS(ctx, obj, managedPods, out, log); err != nil {
					return nil, types.ErrorResult(err)
				} else if stop != nil {
					return nil, *stop
				}
			} else if obj.Status.ServerCA != "" && len(podsWithoutTLS(managedPods)) == len(managedPods) {
				// TLS was turned off and no TLS pod remains; don't keep pointing clients at a pinned cert.
				if err := r.clearTLSStatus(ctx, obj); err != nil {
					return nil, types.ErrorResult(err)
				}
			}

			// Ensure we have exactly one Buildkit pod running, creating or deleting as necessary
			pod, err := r.ensureExactlyOnePod(ctx, obj, managedPods, tlsOpts, out, log)
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

			// A TLS pod being replaced (for a new certificate) may still look ready while it terminates;
			// don't publish an endpoint whose certificate no longer matches status.
			if tlsOpts != nil && pod.DeletionTimestamp != nil {
				return nil, types.RequeueResultWithReasonAndBackoff("Buildkit pod terminating", "PodTerminating")
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
						Reason:  "PodFailed",
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
			if len(pod.Spec.Containers) == 0 || len(pod.Spec.Containers[0].Ports) == 0 {
				log.Errorw("Buildkit pod does not have containers with ports defined", "pod", pod.Name)
				return nil, types.ErrorResult(fmt.Errorf("buildkit pod %s does not have containers with ports defined", pod.Name))
			}
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
func (r *reconciler) ensureExactlyOnePod(ctx context.Context, obj *v1alpha1.Buildkit, managedPods []corev1.Pod, tlsOpts *TLSOptions, out *types.OutputSet, log *zap.SugaredLogger) (*corev1.Pod, error) {
	if len(managedPods) > 1 {
		log.Warnw("Multiple Buildkit pods found, deleting extras", "count", len(managedPods))
		for _, pod := range managedPods[1:] {
			out.Delete(&pod)
		}

		return &managedPods[0], nil
	}

	if len(managedPods) == 0 {
		// No pod running yet, so create one
		pod, err := NewBuilder(obj, r.c.Client).WithTLS(tlsOpts).BuildPod(ctx)
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

func (r *reconciler) clearTLSStatus(ctx context.Context, obj *v1alpha1.Buildkit) error {
	p := []byte(`{"status":{"serverCA":null,"serverName":null}}`)
	if err := r.c.Status().Patch(ctx, obj, client.RawPatch(k8stypes.MergePatchType, p)); err != nil {
		return fmt.Errorf("clearing TLS status: %w", err)
	}

	obj.Status.ServerCA = ""
	obj.Status.ServerName = ""
	return nil
}

// prepareTLS readies a Buildkit whose template requires client TLS. It returns the pod's TLS options,
// or a terminal result when the Buildkit must not run.
func (r *reconciler) prepareTLS(ctx context.Context, obj *v1alpha1.Buildkit, managedPods []corev1.Pod, out *types.OutputSet, log *zap.SugaredLogger) (*TLSOptions, *types.Result, error) {
	// A pod started before the template required TLS listens in plaintext and can't be upgraded in
	// place, so remove it. A TLS pod replaces it if the Buildkit has a client CA.
	if plaintext := podsWithoutTLS(managedPods); len(plaintext) > 0 {
		log.Infow("Removing plaintext Buildkit pods because their template requires client TLS", "count", len(plaintext))
		if err := r.forceDeletePods(ctx, plaintext, out); err != nil {
			return nil, nil, err
		}
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, nil, err
		}
		return nil, &types.Result{Done: true, RequeueAfterCompletion: true, RequeueMsg: "Removing plaintext pod", Reason: reasonApplyingChanges}, nil
	}

	if obj.Spec.ClientCA == "" {
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, nil, err
		}
		return nil, &types.Result{
			Done: true,
			CustomStatusCondition: &types.ResultStatusCondition{
				Reason:  ReasonClientCARequired,
				Status:  corev1.ConditionFalse,
				Message: fmt.Sprintf("BuildkitTemplate %s requires client TLS but spec.clientCA is empty", obj.Spec.Template),
			},
		}, nil
	}

	tlsOpts, serverCA, created, err := r.ensureTLSSecret(ctx, obj, log)
	if err != nil {
		// Stop advertising an instance whose TLS material can no longer be vouched for.
		if clearErr := r.clearConnectionStatus(ctx, obj); clearErr != nil {
			return nil, nil, errors.Join(err, clearErr)
		}
		return nil, nil, err
	}

	// A new Secret means a new server certificate, which a running pod hasn't loaded. Restart it so
	// the certificate in status matches what buildkitd serves.
	if created && len(managedPods) > 0 {
		log.Infow("Restarting Buildkit pods to load a new server certificate", "count", len(managedPods))
		if err := r.forceDeletePods(ctx, managedPods, out); err != nil {
			return nil, nil, err
		}
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, nil, err
		}
		return nil, &types.Result{Done: true, RequeueAfterCompletion: true, RequeueMsg: "Restarting pod for new server certificate", Reason: reasonApplyingChanges}, nil
	}

	obj.Status.ServerCA = serverCA
	obj.Status.ServerName = obj.Name

	return tlsOpts, nil, nil
}

// forceDeletePods deletes pods with a one-second grace period. A pod terminating on the template's
// grace period would keep serving (plaintext, or with a certificate no longer in status) while its
// pre-stop script waits, and the controller would keep treating it as the live instance. A zero grace
// period would instead remove the pod object before kubelet has stopped the container.
func (r *reconciler) forceDeletePods(ctx context.Context, pods []corev1.Pod, out *types.OutputSet) error {
	for i := range pods {
		if err := r.c.Delete(ctx, &pods[i], client.GracePeriodSeconds(1)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting pod %s: %w", pods[i].Name, err)
		}
		out.Delete(&pods[i])
	}
	return nil
}

// templateRequiresTLS reports whether the Buildkit's template requires client TLS. A missing template
// reports false; BuildPod fails on it before any pod could be created.
func (r *reconciler) templateRequiresTLS(ctx context.Context, obj *v1alpha1.Buildkit) (bool, error) {
	var template v1alpha1.BuildkitTemplate
	if err := r.c.Get(ctx, client.ObjectKey{Namespace: obj.Namespace, Name: obj.Spec.Template}, &template); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting BuildkitTemplate %s: %w", obj.Spec.Template, err)
	}

	return template.Spec.RequireClientTLS, nil
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

// errTLSSecretUnusable is deliberately vague: the error lands in a status condition every tenant can
// read, and the details (does a Secret with this name exist?) are only logged.
var errTLSSecretUnusable = errors.New("TLS secret for this Buildkit is unusable; see operator logs")

// ensureTLSSecret returns the TLS options for the Buildkit's pod and the server certificate clients
// should trust, creating the Secret with a fresh server certificate on first use (created is then true).
func (r *reconciler) ensureTLSSecret(ctx context.Context, obj *v1alpha1.Buildkit, log *zap.SugaredLogger) (opts *TLSOptions, serverCA string, created bool, err error) {
	if r.tls.SecretClient == nil || r.tls.HealthProxyImage == "" || !slices.Contains(r.tls.Namespaces, obj.Namespace) {
		return nil, "", false, fmt.Errorf("operator is not configured to manage TLS secrets in namespace %s", obj.Namespace)
	}

	key := client.ObjectKey{Namespace: obj.Namespace, Name: obj.Name + "-tls"}

	var secret corev1.Secret
	err = r.tls.SecretClient.Get(ctx, key, &secret)
	if apierrors.IsNotFound(err) {
		if err := r.createTLSSecret(ctx, obj, key, &secret, log); err != nil {
			return nil, "", false, err
		}
		created = true
	} else if err != nil {
		return nil, "", false, fmt.Errorf("getting TLS secret %s: %w", key.Name, err)
	}

	// Only trust a Secret this Buildkit created and whose client CA still matches its (immutable) spec.
	var problem string
	switch {
	case !metav1.IsControlledBy(&secret, obj):
		problem = "exists but is not controlled by this Buildkit"
	case string(secret.Data[SecretClientCAKey]) != obj.Spec.ClientCA:
		problem = "client CA does not match spec.clientCA"
	case len(secret.Data[corev1.TLSCertKey]) == 0 || len(secret.Data[corev1.TLSPrivateKeyKey]) == 0:
		problem = "is missing its server certificate or key"
	}
	if problem != "" {
		log.Errorw("Refusing to use TLS secret", "secret", key.Name, "problem", problem)
		return nil, "", false, errTLSSecretUnusable
	}

	return &TLSOptions{SecretName: key.Name, HealthProxyImage: r.tls.HealthProxyImage}, string(secret.Data[corev1.TLSCertKey]), created, nil
}

// createTLSSecret creates the Buildkit's TLS Secret with a fresh server certificate, storing the
// result in secret.
func (r *reconciler) createTLSSecret(ctx context.Context, obj *v1alpha1.Buildkit, key client.ObjectKey, secret *corev1.Secret, log *zap.SugaredLogger) error {
	certPEM, keyPEM, err := tlsutil.GenerateServerCert(obj.Name, serverCertValidity, time.Now())
	if err != nil {
		return fmt.Errorf("generating server certificate: %w", err)
	}

	*secret = corev1.Secret{
		Name:      key.Name,
		Namespace: key.Namespace,
		Labels:    map[string]string{TLSSecretLabel: "true"},
		Type:      corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       certPEM,
			corev1.TLSPrivateKeyKey: keyPEM,
			SecretClientCAKey:       []byte(obj.Spec.ClientCA),
		},
	}
	if err := controllerutil.SetControllerReference(obj, secret, r.scheme); err != nil {
		return fmt.Errorf("setting owner on TLS secret: %w", err)
	}

	log.Infow("Creating TLS secret", "secret", key.Name)
	if err := r.tls.SecretClient.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			log.Errorw("TLS secret appeared while creating it", "secret", key.Name)
			return errTLSSecretUnusable
		}
		return fmt.Errorf("creating TLS secret %s: %w", key.Name, err)
	}

	return nil
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
	tlsConfig TLSConfig,
) error {
	_, log, err := logging.ControllerCtx(ctx, controllerName)
	if err != nil {
		return err
	}

	r := &reconciler{
		c:      c,
		scheme: mgr.GetScheme(),
		log:    log,
		tls:    tlsConfig,
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
