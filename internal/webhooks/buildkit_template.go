// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package webhooks

import (
	"context"
	"errors"
	"fmt"

	"github.com/BurntSushi/toml"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-buildkit-seatgeek-io-v1alpha1-buildkittemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=buildkit.seatgeek.io,resources=buildkittemplates,verbs=create;update,versions=v1alpha1,name=mbuildkittemplate.kb.io,admissionReviewVersions=v1

type BuildkitTemplateValidator struct{}

var _ webhook.CustomValidator = (*BuildkitTemplateValidator)(nil)

func (v *BuildkitTemplateValidator) validate(obj runtime.Object) (admission.Warnings, error) {
	bkt, ok := obj.(*v1alpha1.BuildkitTemplate)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected BuildkitTemplate object but got %T", obj))
	}

	var errorList field.ErrorList

	// Validate the BuildkitTemplate name
	if len(bkt.Name) > v1alpha1.BuildkitTemplateNameMaxLength {
		errorList = append(errorList, field.TooLong(
			field.NewPath("metadata", "name"),
			bkt.Name,
			v1alpha1.BuildkitTemplateNameMaxLength,
		))
	}

	// Validate the port number
	if bkt.Spec.Port < 1 || bkt.Spec.Port > 65535 {
		errorList = append(errorList, field.Invalid(
			field.NewPath("spec", "port"),
			bkt.Spec.Port,
			"spec.port must be between 1 and 65535",
		))
	}

	// Validate the PodTemplate
	if bkt.Spec.PodTemplate.Name != "" {
		errorList = append(errorList, field.Invalid(
			field.NewPath("spec", "podTemplate", "name"),
			bkt.Spec.PodTemplate.Name,
			"spec.podTemplate.name must not be set, as pod names are automatically generated",
		))
	}

	// Validate the toml syntax
	var tmp any
	if _, err := toml.Decode(bkt.Spec.BuildkitdToml, &tmp); err != nil {
		reason := "invalid TOML syntax"

		var perr toml.ParseError
		if errors.As(err, &perr) {
			reason = perr.ErrorWithPosition()
		}

		errorList = append(errorList, field.Invalid(
			field.NewPath("spec", "buildkitToml"),
			bkt.Spec.BuildkitdToml,
			reason,
		))
	}

	if len(errorList) > 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{
				Group: v1alpha1.GroupVersion.Group,
				Kind:  "BuildkitTemplate",
			},
			bkt.Name,
			errorList,
		)
	}

	return nil, nil
}

func (v *BuildkitTemplateValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	return v.validate(obj)
}

func (v *BuildkitTemplateValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	return v.validate(newObj)
}

func (v *BuildkitTemplateValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// No validation needed on delete
	return nil, nil
}

// +kubebuilder:webhook:path=/mutate-buildkit-seatgeek-io-v1alpha1-buildkittemplate,mutating=true,failurePolicy=fail,sideEffects=None,groups=buildkit.seatgeek.io,resources=buildkittemplates,verbs=create;update,versions=v1alpha1,name=mbuildkittemplate.kb.io,admissionReviewVersions=v1

type BuildkitTemplateDefaulter struct{}

func (b BuildkitTemplateDefaulter) Default(_ context.Context, obj runtime.Object) error {
	bkt, ok := obj.(*v1alpha1.BuildkitTemplate)
	if !ok {
		return fmt.Errorf("expected BuildkitTemplate object, got %T", obj)
	}

	if bkt.Spec.Port == 0 {
		bkt.Spec.Port = 1234
	}

	if len(bkt.Spec.PodTemplate.Spec.Containers) == 0 {
		bkt.Spec.PodTemplate.Spec.Containers = make([]corev1.Container, 1)
	}

	if bkt.Spec.PodTemplate.Spec.Containers[0].Image == "" {
		bkt.Spec.PodTemplate.Spec.Containers[0].Image = "moby/buildkit:latest"
	}

	return nil
}

var _ webhook.CustomDefaulter = (*BuildkitTemplateDefaulter)(nil)
