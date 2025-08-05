// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package webhooks

import (
	"context"
	"fmt"
	"reflect"

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

func (v *BuildkitValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	bk, ok := obj.(*v1alpha1.Buildkit)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected Buildkit object but got %T", obj))
	}

	var errorList field.ErrorList

	var template v1alpha1.BuildkitTemplate

	// Validate template
	if bk.Spec.Template == "" {
		errorList = append(errorList, field.Required(field.NewPath("spec", "template"), "BuildkitTemplate name must be specified"))
	} else if err := v.c.Get(ctx, client.ObjectKey{Namespace: bk.Namespace, Name: bk.Spec.Template}, &template); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, apierrors.NewInternalError(fmt.Errorf("failed to get BuildkitTemplate '%s' in namespace '%s': %w", bk.Spec.Template, bk.Namespace, err))
		}
		errorList = append(errorList, field.NotFound(field.NewPath("spec", "template"), bk.Spec.Template))
	} else if template.Spec.Lifecycle.RequireOwner && len(bk.GetOwnerReferences()) == 0 {
		errorList = append(errorList, field.Required(
			field.NewPath("metadata", "ownerReferences"),
			fmt.Sprintf("BuildkitTemplate '%s' requires owner references but none are present", bk.Spec.Template),
		))
	}

	if len(errorList) > 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{
				Group: v1alpha1.SchemeGroupVersion.Group,
				Kind:  "Buildkit",
			},
			bk.Name,
			errorList,
		)
	}

	return nil, nil
}

func (v *BuildkitValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldBk, ok := oldObj.(*v1alpha1.Buildkit)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected Buildkit object but got %T", oldObj))
	}

	newBk, ok := newObj.(*v1alpha1.Buildkit)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected Buildkit object but got %T", newObj))
	}

	if !reflect.DeepEqual(oldBk.Spec, newBk.Spec) {
		return nil, apierrors.NewBadRequest("spec changes are not allowed for existing Buildkit objects")
	}

	return nil, nil
}

func (v *BuildkitValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// No validation needed on delete
	return nil, nil
}
