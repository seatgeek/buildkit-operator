// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package merge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seatgeek/buildkit-operator/internal/merge"
)

func TestMaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []map[string]string
		expected map[string]string
	}{
		{
			name:     "empty map",
			input:    nil,
			expected: map[string]string{},
		},
		{
			name: "merges multiple maps",
			input: []map[string]string{
				{"hello": "world"},
				{"foo": "bar"},
				{"hello": "everyone"},
			},
			expected: map[string]string{
				"hello": "everyone", // Last one wins
				"foo":   "bar",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := merge.Maps(tt.input...)
			assert.Equal(t, tt.expected, result)
		})
	}
}
