// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package buildkit_template

import (
	"github.com/reddit/achilles-sdk-api/api"
	corev1 "k8s.io/api/core/v1"
)

var conditionReady = api.Condition{
	Type:   api.TypeReady,
	Status: corev1.ConditionTrue,
	Reason: api.ReasonAvailable,
}
