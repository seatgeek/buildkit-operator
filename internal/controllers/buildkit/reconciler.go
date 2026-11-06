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

// TLSConfig configures how the controller provisions mutual TLS for templates that require it.
type TLSConfig struct {
	// SecretClient reads, creates and deletes TLS Secrets, and re-reads Buildkits when the cache may be
	// stale. It must not be backed by the manager's cache, so the operator never lists or caches Secrets.
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
	now    func() time.Time
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

			var tlsOpts *TLSOptions
			if requireTLS {
				var stop *types.Result
				if tlsOpts, stop, err = r.prepareTLS(ctx, obj, template, managedPods, out, log); err != nil {
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

			// A terminating TLS pod may still look ready, or be held by a finalizer; stop advertising it
			// rather than let clients connect to an instance on its way out.
			if tlsOpts != nil && pod.DeletionTimestamp != nil {
				if err := r.clearConnectionStatus(ctx, obj); err != nil {
					return nil, types.ErrorResult(err)
				}
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

			if tlsOpts != nil {
				if stop, err := r.reissueIfCertExpiresFirst(ctx, obj, template, pod, log); err != nil {
					return nil, types.ErrorResult(err)
				} else if stop != nil {
					return nil, *stop
				}
			}

			obj.Status.Endpoint = fmt.Sprintf("tcp://%s", net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(pod.Spec.Containers[0].Ports[0].ContainerPort))))

			if tlsOpts != nil {
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

// prepareTLS readies a Buildkit whose template requires client TLS. It returns the pod's TLS options,
// or a terminal result when the Buildkit must not run.
func (r *reconciler) prepareTLS(ctx context.Context, obj *v1alpha1.Buildkit, template *v1alpha1.BuildkitTemplate, managedPods []corev1.Pod, out *types.OutputSet, log *zap.SugaredLogger) (*TLSOptions, *types.Result, error) {
	// The pod may have vanished before this controller saw it terminating; don't leave its endpoint
	// advertised alongside the certificate prepared for its replacement.
	if len(managedPods) == 0 {
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, nil, err
		}
	}

	// A pod started before the template required TLS listens in plaintext and can't be upgraded in
	// place, so remove it. A TLS pod replaces it if the Buildkit has a client CA.
	if plaintext := podsWithoutTLS(managedPods); len(plaintext) > 0 {
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, nil, err
		}
		if deleting := notYetDeleting(plaintext); len(deleting) > 0 {
			log.Infow("Removing plaintext Buildkit pods because their template requires client TLS", "count", len(deleting))
			if err := r.forceDeletePods(ctx, deleting, out); err != nil {
				return nil, nil, err
			}
			return nil, &types.Result{Done: true, RequeueAfterCompletion: true, RequeueMsg: "Removing plaintext pod", Reason: reasonApplyingChanges}, nil
		}
		result := types.RequeueResultWithReasonAndBackoff("Buildkit pod terminating", "PodTerminating")
		return nil, &result, nil
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

	// The webhook enforces this too; checking here keeps the controller safe without it, and catches a
	// client CA that has expired since (no client could connect with it).
	if problem := clientCAProblem(obj.Spec.ClientCA, r.now()); problem != "" {
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, nil, err
		}
		return nil, &types.Result{
			Done: true,
			CustomStatusCondition: &types.ResultStatusCondition{
				Reason:  ReasonClientCAInvalid,
				Status:  corev1.ConditionFalse,
				Message: "spec.clientCA " + problem,
			},
		}, nil
	}

	// A certificate for a pod about to be created must outlive it; a running pod's was checked when it
	// started, so then the certificate only needs to be unexpired.
	var validFor time.Duration
	if len(managedPods) == 0 {
		validFor = serverCertLifetime(template)
	}
	tlsOpts, serverCA, err := r.ensureTLSSecret(ctx, obj, template, validFor, log)
	if err != nil {
		// Stop advertising an instance whose TLS material can no longer be vouched for.
		if clearErr := r.clearConnectionStatus(ctx, obj); clearErr != nil {
			return nil, nil, errors.Join(err, clearErr)
		}
		return nil, nil, err
	}

	// A pod mounting a different Secret was started with a different server certificate (one was
	// re-minted, possibly by an earlier reconcile that didn't get to restart it), which clients can't
	// verify against status. Drop its Secret and restart it.
	if stale := podsWithStaleSecret(managedPods, tlsOpts.SecretName); len(stale) > 0 {
		if err := r.clearConnectionStatus(ctx, obj); err != nil {
			return nil, nil, err
		}
		if err := r.deleteSupersededSecrets(ctx, obj, stale, tlsOpts.SecretName, log); err != nil {
			return nil, nil, err
		}
		if deleting := notYetDeleting(stale); len(deleting) > 0 {
			log.Infow("Restarting Buildkit pods to load the current server certificate", "count", len(deleting))
			if err := r.forceDeletePods(ctx, deleting, out); err != nil {
				return nil, nil, err
			}
			return nil, &types.Result{Done: true, RequeueAfterCompletion: true, RequeueMsg: "Restarting pod for new server certificate", Reason: reasonApplyingChanges}, nil
		}
		// Already terminating; wait for it to go before starting the replacement.
		result := types.RequeueResultWithReasonAndBackoff("Buildkit pod terminating", "PodTerminating")
		return nil, &result, nil
	}

	obj.Status.ServerCA = serverCA
	obj.Status.ServerName = obj.Name

	return tlsOpts, nil, nil
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

// podsWithStaleSecret returns the pods that don't mount the current TLS Secret.
func podsWithStaleSecret(pods []corev1.Pod, current string) []corev1.Pod {
	var out []corev1.Pod
	for _, pod := range pods {
		if podTLSSecretName(&pod) != current {
			out = append(out, pod)
		}
	}
	return out
}

// deleteSupersededSecrets deletes the TLS Secrets stale pods mount, if this Buildkit controls them. A
// running pod keeps its mounted copy, so this is safe before the pod goes, and doing it first means a
// crash in between leaves no Secret that only garbage collection would find.
func (r *reconciler) deleteSupersededSecrets(ctx context.Context, obj *v1alpha1.Buildkit, stale []corev1.Pod, current string, log *zap.SugaredLogger) error {
	for i := range stale {
		name := podTLSSecretName(&stale[i])
		if name == "" || name == current {
			continue
		}
		var secret corev1.Secret
		if err := r.tls.SecretClient.Get(ctx, client.ObjectKey{Namespace: obj.Namespace, Name: name}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("getting superseded TLS secret %s: %w", name, err)
		}
		if !metav1.IsControlledBy(&secret, obj) {
			continue
		}
		log.Infow("Deleting superseded TLS secret", "secret", name)
		if err := r.tls.SecretClient.Delete(ctx, &secret, client.Preconditions{UID: &secret.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting superseded TLS secret %s: %w", name, err)
		}
	}
	return nil
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

// reissueIfCertExpiresFirst discards the server certificate if it would expire before the pod must
// have stopped, so the next reconcile mints a longer-lived one and restarts the pod. Nothing has
// connected yet: this runs before the pod is first advertised.
func (r *reconciler) reissueIfCertExpiresFirst(ctx context.Context, obj *v1alpha1.Buildkit, template *v1alpha1.BuildkitTemplate, pod *corev1.Pod, log *zap.SugaredLogger) (*types.Result, error) {
	outlived, err := r.certOutlivesPod(obj, template, pod)
	if err != nil || outlived {
		return nil, err
	}

	log.Infow("Server certificate would expire before the pod's deadline; issuing a new one", "pod", pod.Name)
	if err := r.clearConnectionStatus(ctx, obj); err != nil {
		return nil, err
	}
	p := []byte(`{"status":{"serverCertSHA256":null}}`)
	if err := r.c.Status().Patch(ctx, obj, client.RawPatch(k8stypes.MergePatchType, p)); err != nil {
		return nil, fmt.Errorf("discarding server certificate fingerprint: %w", err)
	}
	// Decoding the response leaves omitted fields as they were; without this, the FSM's final status
	// write would put the fingerprint back.
	obj.Status.ServerCertSHA256 = ""
	return &types.Result{Done: true, RequeueAfterCompletion: true, RequeueMsg: "Issuing a longer-lived server certificate", Reason: reasonApplyingChanges}, nil
}

// certOutlivesPod reports whether the advertised server certificate lasts until the pod must have
// stopped: its actual start plus its own deadline and grace period. The certificate is issued with a margin
// for scheduling delay, but a pod can wait in Pending for arbitrarily long.
func (r *reconciler) certOutlivesPod(obj *v1alpha1.Buildkit, template *v1alpha1.BuildkitTemplate, pod *corev1.Pod) (bool, error) {
	notAfter, err := tlsutil.NotAfter([]byte(obj.Status.ServerCA))
	if err != nil {
		return false, fmt.Errorf("reading server certificate expiry: %w", err)
	}
	started := r.now()
	if pod.Status.StartTime != nil {
		started = pod.Status.StartTime.Time
	}
	return !started.Add(podServingTime(pod, template)).After(notAfter), nil
}

// podServingTime is how long a started pod can serve, from its own spec: a template changed since
// only affects replacement pods.
func podServingTime(pod *corev1.Pod, template *v1alpha1.BuildkitTemplate) time.Duration {
	if pod.Spec.ActiveDeadlineSeconds == nil {
		return tlsPodServingTime(template)
	}
	grace := int64(corev1.DefaultTerminationGracePeriodSeconds)
	if pod.Spec.TerminationGracePeriodSeconds != nil {
		grace = *pod.Spec.TerminationGracePeriodSeconds
	}
	return time.Duration(*pod.Spec.ActiveDeadlineSeconds+grace) * time.Second
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

// errTLSSecretUnusable is deliberately vague: the error lands in a status condition every tenant can
// read, and the details (does a Secret with this name exist?) are only logged.
var errTLSSecretUnusable = errors.New("TLS secret for this Buildkit is unusable; see operator logs")

// tlsSecretName names the Secret holding the server certificate with the given fingerprint. Each
// certificate gets its own Secret, so a name is never reused for different material: kubelet keeps
// serving an immutable Secret it has cached for as long as any pod on the node mounts that name, even
// after the Secret is deleted and recreated.
func tlsSecretName(obj *v1alpha1.Buildkit, fingerprint string) string {
	return obj.Name + "-tls-" + fingerprint[:tlsSecretSuffixLength]
}

// tlsSecretSuffixLength is the number of fingerprint hex characters in a TLS Secret's name. It only
// needs to tell one Buildkit's certificates apart; trust rests on the full fingerprint in status.
const tlsSecretSuffixLength = 16

// ensureTLSSecret returns the TLS options for the Buildkit's pod and the server certificate clients
// should trust. The current Secret is the one named for status.serverCertSHA256, which only the
// operator can write. If it is missing, or the operator can't vouch for its contents (an owner
// reference alone proves nothing: anyone who can create Secrets can forge one), a new certificate is
// minted into a new Secret.
func (r *reconciler) ensureTLSSecret(ctx context.Context, obj *v1alpha1.Buildkit, template *v1alpha1.BuildkitTemplate, validFor time.Duration, log *zap.SugaredLogger) (*TLSOptions, string, error) {
	if r.tls.SecretClient == nil || r.tls.HealthProxyImage == "" || !slices.Contains(r.tls.Namespaces, obj.Namespace) {
		return nil, "", fmt.Errorf("operator is not configured to manage TLS secrets in namespace %s", obj.Namespace)
	}

	// The fingerprint is patched straight after minting, so the informer cache can lag it. Acting on a
	// stale one could mint again, or point back at a superseded Secret and write it back to status.
	var fresh v1alpha1.Buildkit
	if err := r.tls.SecretClient.Get(ctx, client.ObjectKeyFromObject(obj), &fresh); err != nil {
		return nil, "", fmt.Errorf("re-reading Buildkit %s: %w", obj.Name, err)
	}
	obj.Status.ServerCertSHA256 = fresh.Status.ServerCertSHA256

	secret, found, err := r.currentTLSSecret(ctx, obj, validFor, log)
	if err != nil {
		return nil, "", err
	}
	if !found {
		if secret, err = r.mintTLSSecret(ctx, obj, serverCertLifetime(template), log); err != nil {
			return nil, "", err
		}
	}

	return &TLSOptions{SecretName: secret.Name, HealthProxyImage: r.tls.HealthProxyImage}, string(secret.Data[corev1.TLSCertKey]), nil
}

// currentTLSSecret returns the Secret for the recorded fingerprint if the operator can vouch for it
// and its certificate lasts at least validFor more; found is false when a new certificate must be
// minted. An unvouched Secret claiming this Buildkit is deleted;
// one that doesn't claim it is left alone.
func (r *reconciler) currentTLSSecret(ctx context.Context, obj *v1alpha1.Buildkit, validFor time.Duration, log *zap.SugaredLogger) (secret *corev1.Secret, found bool, err error) {
	if obj.Status.ServerCertSHA256 == "" {
		return nil, false, nil
	}

	key := client.ObjectKey{Namespace: obj.Namespace, Name: tlsSecretName(obj, obj.Status.ServerCertSHA256)}
	secret = &corev1.Secret{}
	if err := r.tls.SecretClient.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("getting TLS secret %s: %w", key.Name, err)
	}

	if !metav1.IsControlledBy(secret, obj) {
		log.Infow("Not using TLS secret", "secret", key.Name, "problem", "not controlled by this Buildkit")
		return nil, false, nil
	}
	problem := trustProblem(obj, secret)
	if problem == "" {
		if notAfter, _ := tlsutil.NotAfter(secret.Data[corev1.TLSCertKey]); r.now().Add(validFor).After(notAfter) {
			problem = "server certificate expires before the pod would finish"
		}
	}
	if problem != "" {
		log.Infow("Replacing TLS secret", "secret", key.Name, "problem", problem)
		if err := r.tls.SecretClient.Delete(ctx, secret, client.Preconditions{UID: &secret.UID}); err != nil && !apierrors.IsNotFound(err) {
			return nil, false, fmt.Errorf("deleting TLS secret %s: %w", key.Name, err)
		}
		return nil, false, nil
	}

	return secret, true, nil
}

// trustProblem explains why the operator can't vouch for a Secret claiming this Buildkit as owner, or
// returns "" if it can.
func trustProblem(obj *v1alpha1.Buildkit, secret *corev1.Secret) string {
	certPEM, keyPEM := secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey]
	fingerprint, err := tlsutil.Fingerprint(certPEM)
	switch {
	case err != nil:
		return "server certificate is unreadable"
	case fingerprint != obj.Status.ServerCertSHA256:
		return "server certificate is not the one the operator recorded"
	case string(secret.Data[SecretClientCAKey]) != obj.Spec.ClientCA:
		return "client CA does not match spec.clientCA"
	case secret.Immutable == nil || !*secret.Immutable:
		return "secret is mutable"
	}
	if err := tlsutil.ValidateServerKeyPair(certPEM, keyPEM, obj.Name); err != nil {
		return "server certificate and key are unusable"
	}
	return ""
}

// mintTLSSecret generates a server certificate, stores it in a new Secret named for its fingerprint,
// and records the fingerprint in status before anything can use the Secret.
func (r *reconciler) mintTLSSecret(ctx context.Context, obj *v1alpha1.Buildkit, validity time.Duration, log *zap.SugaredLogger) (*corev1.Secret, error) {
	certPEM, keyPEM, err := tlsutil.GenerateServerCert(obj.Name, validity, r.now())
	if err != nil {
		return nil, fmt.Errorf("generating server certificate: %w", err)
	}
	fingerprint, err := tlsutil.Fingerprint(certPEM)
	if err != nil {
		return nil, fmt.Errorf("fingerprinting server certificate: %w", err)
	}

	secret := &corev1.Secret{
		Name:      tlsSecretName(obj, fingerprint),
		Namespace: obj.Namespace,
		Labels:    map[string]string{TLSSecretLabel: "true"},
		Type:      corev1.SecretTypeTLS,
		// Immutable so the material can't be patched between validation and a pod mounting it. Changing
		// it means deleting and recreating the Secret, which the fingerprint check catches.
		Immutable: new(true),
		Data: map[string][]byte{
			corev1.TLSCertKey:       certPEM,
			corev1.TLSPrivateKeyKey: keyPEM,
			SecretClientCAKey:       []byte(obj.Spec.ClientCA),
		},
	}
	if err := controllerutil.SetControllerReference(obj, secret, r.scheme); err != nil {
		return nil, fmt.Errorf("setting owner on TLS secret: %w", err)
	}

	log.Infow("Creating TLS secret", "secret", secret.Name)
	if err := r.tls.SecretClient.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The name comes from a certificate generated just now, so it can't have been predicted.
			log.Errorw("TLS secret appeared while creating it", "secret", secret.Name)
			return nil, errTLSSecretUnusable
		}
		return nil, fmt.Errorf("creating TLS secret %s: %w", secret.Name, err)
	}

	// Persist the fingerprint now rather than with the rest of status at the end of the reconcile. If
	// the operator stopped in between, the next reconcile would mint again, leaving this Secret for
	// garbage collection with the Buildkit.
	p := fmt.Appendf(nil, `{"status":{"serverCertSHA256":%q}}`, fingerprint)
	if err := r.c.Status().Patch(ctx, obj, client.RawPatch(k8stypes.MergePatchType, p)); err != nil {
		return nil, fmt.Errorf("recording server certificate fingerprint: %w", err)
	}
	obj.Status.ServerCertSHA256 = fingerprint

	return secret, nil
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
		now:    time.Now,
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
