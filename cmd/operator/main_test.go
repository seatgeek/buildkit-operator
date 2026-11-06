// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootCommand_rejectsInvalidHelperPullPolicy(t *testing.T) {
	t.Parallel()

	cmd := rootCommand(t.Context())
	cmd.SetArgs([]string{"--health-proxy-image-pull-policy=Sometimes"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	require.ErrorContains(t, cmd.Execute(), `invalid --health-proxy-image-pull-policy "Sometimes"`)
}
