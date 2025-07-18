// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package webhooks

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-buildkit-seatgeek-io-v1alpha1-buildkit,mutating=false,failurePolicy=fail,sideEffects=None,groups=buildkit.seatgeek.io,resources=buildkits,verbs=create;update,versions=v1alpha1,name=mbuildkit.kb.io,admissionReviewVersions=v1

type BuildkitValidator struct {
	c client.Reader
}

var _ webhook.CustomValidator = (*BuildkitValidator)(nil)

func NewBuildkitValidator(c client.Reader) *BuildkitValidator {
	return &BuildkitValidator{
		c: c,
	}
}

func (v *BuildkitValidator) validate(obj runtime.Object) (admission.Warnings, error) {
	bk, ok := obj.(*v1alpha1.Buildkit)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected Buildkit object but got %T", obj))
	}

	var errorList field.ErrorList

	if bk.Spec.Template == "" {
		errorList = append(errorList, field.Required(field.NewPath("spec", "template"), "BuildkitTemplate name must be specified"))
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		// Ensure the referenced BuildkitTemplate exists
		var template v1alpha1.BuildkitTemplate
		err := v.c.Get(ctx, client.ObjectKey{Namespace: bk.Namespace, Name: bk.Spec.Template}, &template)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, apierrors.NewInternalError(fmt.Errorf("failed to get BuildkitTemplate '%s' in namespace '%s': %w", bk.Spec.Template, bk.Namespace, err))
			}

			errorList = append(errorList, &field.Error{
				Type:     field.ErrorTypeNotFound,
				Field:    field.NewPath("spec", "template").String(),
				BadValue: bk.Spec.Template,
				Detail:   fmt.Sprintf("BuildkitTemplate '%s' not found in namespace '%s'", bk.Spec.Template, bk.Namespace),
			})
		}
	}

	if len(errorList) > 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{
				Group: v1alpha1.GroupVersion.Group,
				Kind:  "Buildkit",
			},
			bk.Name,
			errorList,
		)
	}

	return nil, nil
}

func (v *BuildkitValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	return v.validate(obj) //nolint:contextcheck
}

func (v *BuildkitValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	return v.validate(newObj) //nolint:contextcheck
}

func (v *BuildkitValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// No validation needed on delete
	return nil, nil
}
