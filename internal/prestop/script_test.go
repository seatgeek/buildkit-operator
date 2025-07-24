// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package prestop_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seatgeek/buildkit-operator/internal/prestop"
)

func TestScript(t *testing.T) {
	t.Parallel()

	tests := []struct {
		port int32
	}{
		{port: 1234},
		{port: 5678},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("port %d", tt.port), func(t *testing.T) {
			t.Parallel()

			script := prestop.Script(tt.port)
			assert.Contains(t, script, "#!/bin/sh")
			assert.Contains(t, script, fmt.Sprintf("BUILDKITD_PORT=%d", tt.port))
		})
	}
}
