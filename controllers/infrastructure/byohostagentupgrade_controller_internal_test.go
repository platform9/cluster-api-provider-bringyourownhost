// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClampToInt32(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int32
	}{
		{name: "zero", input: 0, want: 0},
		{name: "typical host count", input: 7, want: 7},
		{name: "at upper bound", input: math.MaxInt32, want: math.MaxInt32},
		{name: "above upper bound saturates", input: math.MaxInt32 + 1, want: math.MaxInt32},
		{name: "at lower bound", input: math.MinInt32, want: math.MinInt32},
		{name: "below lower bound saturates", input: math.MinInt32 - 1, want: math.MinInt32},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := clampToInt32(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}
