// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package merge

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

// Objects merges `overrides` into `base` using Kubernetes Strategic Merge Patch (SMP).
// This performs ADDITIVE merging - it never deletes fields from base that are missing
// in overrides, it only adds new fields or modifies existing ones.
//
// Key behaviors:
// - Fields in base that are missing from override are PRESERVED (no deletions)
// - Fields in override that don't exist in base are ADDED
// - Fields that exist in both are UPDATED with override's value
// - Arrays are merged according to their `patchStrategy` struct tags (merge vs replace)
// - Multiple overrides are applied in sequence
//
// This is ideal for layered configuration where you want to apply overrides on top
// of defaults without losing any default values.
//
// Adapted from https://github.com/cisco-open/operator-tools/blob/c07faa6d92b8102360a72bf478d8acf08b6fc76b/pkg/merge/merge_test.go
// (c) Banzai Cloud; licensed under the Apache License 2.0
func Objects[T any](base *T, overrides ...T) error {
	for _, override := range overrides {
		if err := mergeOne(base, override); err != nil {
			return err
		}
	}

	return nil
}

func mergeOne[T any](base *T, override T) error {
	baseBytes, err := json.Marshal(base)
	if err != nil {
		return fmt.Errorf("failed to convert current object to byte sequence: %w", err)
	}

	overrideBytes, err := json.Marshal(override)
	if err != nil {
		return fmt.Errorf("failed to convert current object to byte sequence: %w", err)
	}

	patchMeta, err := strategicpatch.NewPatchMetaFromStruct(base)
	if err != nil {
		return fmt.Errorf("failed to produce patch meta from struct: %w", err)
	}

	// Create an additive merge patch that preserves existing fields in base.
	// We use CreateThreeWayMergePatch with identical original/modified parameters
	// to get the effect of IgnoreDeletions=true, which ensures we never delete
	// fields from base that are missing in override - we only add or modify fields.
	//
	// This is equivalent to a two-way merge with DiffOptions{IgnoreDeletions: true}
	// (but the Kubernetes strategic patch API doesn't expose DiffOptions directly).
	//
	// The three-way merge algorithm computes:
	// - deletions = diffMaps(original=override, modified=override) = empty (no changes)
	// - additions = diffMaps(current=base, modified=override, IgnoreDeletions=true)
	// - result = merge(deletions=empty, additions) = additions only
	patch, err := strategicpatch.CreateThreeWayMergePatch(overrideBytes, overrideBytes, baseBytes, patchMeta, true)
	if err != nil {
		return fmt.Errorf("failed to create merge patch: %w", err)
	}

	merged, err := strategicpatch.StrategicMergePatchUsingLookupPatchMeta(baseBytes, patch, patchMeta)
	if err != nil {
		return fmt.Errorf("failed to apply patch: %w", err)
	}

	valueOfBase := reflect.Indirect(reflect.ValueOf(base))
	into := reflect.New(valueOfBase.Type())
	if err := json.Unmarshal(merged, into.Interface()); err != nil {
		return err
	}
	if !valueOfBase.CanSet() {
		return errors.New("unable to set unmarshalled value into base object")
	}

	valueOfBase.Set(reflect.Indirect(into))

	return nil
}
