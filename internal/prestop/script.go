// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package prestop

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed buildkit-prestop.sh
var source string

func Script(port int32) string {
	return strings.Replace(source, "BUILDKITD_PORT=1234", fmt.Sprintf("BUILDKITD_PORT=%d", port), 1)
}
