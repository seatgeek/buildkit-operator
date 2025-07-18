// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package controlplane

import "github.com/reddit/achilles-sdk/pkg/fsm/metrics"

// Context holds information on how the controller should run. These values may
// be referenced during the execution of transition functions.
type Context struct {
	// Metrics is the prometheus metrics sink for this controller binary.
	Metrics *metrics.Metrics
}
