// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package merge

func Maps[K comparable, V any, Map ~map[K]V](maps ...Map) Map { //nolint:ireturn
	size := 0
	for i := range maps {
		size += len(maps[i])
	}

	merged := make(Map, size)
	for i := range maps {
		for k := range maps[i] {
			merged[k] = maps[i][k]
		}
	}

	return merged
}
