// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package webhooks

import (
	ctrl "sigs.k8s.io/controller-runtime"
)

func SetupWebhooks(_ ctrl.Manager) error {
	return nil
}
