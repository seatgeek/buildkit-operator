// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_template

import (
	"context"

	"github.com/reddit/achilles-sdk-api/api"
	"github.com/reddit/achilles-sdk/pkg/fsm"
	"github.com/reddit/achilles-sdk/pkg/fsm/types"
	"github.com/reddit/achilles-sdk/pkg/io"
	"github.com/reddit/achilles-sdk/pkg/logging"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"github.com/seatgeek/buildkit-operator/internal/controlplane"
)

//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkittemplates,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkittemplates/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=buildkit.seatgeek.io,resources=buildkittemplates/finalizers,verbs=update
//+kubebuilder:rbac:resources=configmaps,verbs=get;list;watch;create;update;patch;delete

const controllerName = "BuildkitTemplate"

type state = types.State[*v1alpha1.BuildkitTemplate]

type reconciler struct {
	c      *io.ClientApplicator
	scheme *runtime.Scheme
	log    *zap.SugaredLogger
}

func (r *reconciler) createConfigMap() *state {
	return &state{
		Name:      "create-configmap",
		Condition: conditionReady,
		Transition: func(ctx context.Context, obj *v1alpha1.BuildkitTemplate, out *types.OutputSet) (*state, types.Result) {
			log := r.log.With("name", obj.Name, "namespace", obj.Namespace)

			builder := NewBuilder(obj)
			if configMap := builder.ConfigMap(); configMap != nil {
				log.Debugw("applying configmap", "configmap", configMap.Name)
				out.Apply(configMap)
			} else {
				log.Debugw("removing configmap", "configmap", builder.ConfigMapName())
				out.DeleteByRef(api.TypedObjectRef{
					Version:   "v1",
					Kind:      "ConfigMap",
					Name:      builder.ConfigMapName(),
					Namespace: obj.Namespace,
				})
			}

			return nil, types.DoneResult()
		},
	}
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
		&v1alpha1.BuildkitTemplate{},
		r.createConfigMap(),
		mgr.GetScheme(),
	).Manages(
		corev1.SchemeGroupVersion.WithKind("ConfigMap"),
	)

	return builder.Build()(mgr, log, rl, cpCtx.Metrics)
}
