// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package test

import (
	"path/filepath"
	"runtime"
)

// RootDir returns the root directory of the project
func RootDir() string {
	_, b, _, _ := runtime.Caller(0) //nolint:dogsled
	// this file is located at internal/test, which is two directories from the root
	return filepath.Join(filepath.Dir(b), "../..")
}

// CRDPaths returns the paths to this project's CRD manifests
func CRDPaths() []string {
	return []string{
		filepath.Join(RootDir(), "config", "crd", "bases"),
	}
}

// WebhookPath returns the paths to this project's webhook manifests
func WebhookPath() string {
	return filepath.Join(RootDir(), "config", "webhook")
}
