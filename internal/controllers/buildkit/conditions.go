// Copyright 2025 SeatGeek, Inc.
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
