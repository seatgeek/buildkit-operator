// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package webhooks

import (
	"errors"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

func SetupWebhooks(mgr ctrl.Manager) error {
	return errors.Join(
		ctrl.NewWebhookManagedBy(mgr).For(&v1alpha1.Buildkit{}).
			WithValidator(NewBuildkitValidator(mgr.GetClient())).
			Complete(),
		ctrl.NewWebhookManagedBy(mgr).For(&v1alpha1.BuildkitTemplate{}).
			WithDefaulter(&BuildkitTemplateDefaulter{}).
			WithValidator(&BuildkitTemplateValidator{}).
			Complete(),
	)
}
