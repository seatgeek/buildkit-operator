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

// Objects merges `overrides` into `base` using the SMP (structural merge patch) approach.
// - It intentionally does not remove fields present in base but missing from overrides
// - It merges slices only if the `patchStrategy:"merge"` tag is present and the `patchMergeKey` identifies the unique field
// - Multiple overrides are applied in sequence
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
	patch, err := strategicpatch.CreateThreeWayMergePatch(overrideBytes, overrideBytes, baseBytes, patchMeta, true)
	if err != nil {
		return fmt.Errorf("failed to create three way merge patch: %w", err)
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
