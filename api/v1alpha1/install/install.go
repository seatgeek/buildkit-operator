// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package install

import (
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
)

func Install(scheme *runtime.Scheme) {
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
}
