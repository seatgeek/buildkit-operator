// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit

import (
	"github.com/reddit/achilles-sdk-api/api"
	corev1 "k8s.io/api/core/v1"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

var conditionDeployed = api.Condition{
	Type:   v1alpha1.TypeDeployed,
	Status: corev1.ConditionTrue,
	Reason: api.ReasonAvailable,
}

const (
	// ReasonClientCARequired means the template requires client TLS but the Buildkit has no spec.clientCA.
	ReasonClientCARequired api.ConditionReason = "ClientCARequired"
	// ReasonClientCAInvalid means spec.clientCA can't authenticate clients now: it is not exactly one
	// usable PEM CA certificate, or it has expired or is not yet valid.
	ReasonClientCAInvalid api.ConditionReason = "ClientCAInvalid"
	// ReasonTLSNotConfigured means the template requires client TLS but the operator was started without
	// the image for TLS pods' helper containers.
	ReasonTLSNotConfigured api.ConditionReason = "TLSNotConfigured"
	// ReasonServerCertificateUnusable means a TLS pod reported no server certificate the controller can
	// publish.
	ReasonServerCertificateUnusable api.ConditionReason = "ServerCertificateUnusable"
)
