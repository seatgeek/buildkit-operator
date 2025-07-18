// Copyright 2025 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package matchers

import (
	"fmt"

	"github.com/onsi/gomega/types"
	"github.com/reddit/achilles-sdk-api/api"
)

// MatchCondition is a Gomega matcher that checks if a []api.Condition slices contains the given partial condition.
// Any fields in the expected condition that are empty will be ignored during the match.
func MatchCondition(expectedPartial api.Condition) types.GomegaMatcher { //nolint:ireturn
	return &partialConditionMatcher{
		expectedPartial: expectedPartial,
	}
}

type partialConditionMatcher struct {
	expectedPartial api.Condition
}

func (m *partialConditionMatcher) Match(actual any) (success bool, err error) {
	condition, ok := actual.(api.Condition)
	if !ok {
		return false, fmt.Errorf("MatchCondition expected []api.Condition but got %T", actual)
	}

	return (m.expectedPartial.Type == "" || condition.Type == m.expectedPartial.Type) &&
		(m.expectedPartial.Status == "" || condition.Status == m.expectedPartial.Status) &&
		(m.expectedPartial.Reason == "" || condition.Reason == m.expectedPartial.Reason) &&
		(m.expectedPartial.Message == "" || condition.Message == m.expectedPartial.Message), nil
}

func (m *partialConditionMatcher) FailureMessage(actual any) (message string) {
	return fmt.Sprintf("Expected\n\t%#v\nto contain a condition matching:\n\t%#v", actual, m.expectedPartial)
}

func (m *partialConditionMatcher) NegatedFailureMessage(actual any) (message string) {
	return fmt.Sprintf("Expected\n\t%#v\nto not contain a condition matching:\n\t%#v", actual, m.expectedPartial)
}
