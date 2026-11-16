// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package hostname_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform9/cluster-api-provider-bringyourownhost/common/hostname"
)

func TestNormalize(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "already a valid object name",
			input: "web-01",
			want:  "web-01",
		},
		{
			name:  "uppercase is lowercased",
			input: "WEB-01",
			want:  "web-01",
		},
		{
			name:  "underscores become hyphens",
			input: "web_01",
			want:  "web-01",
		},
		{
			name:  "trailing dot is stripped",
			input: "web-01.",
			want:  "web-01",
		},
		{
			name:  "fully qualified name keeps its interior dots",
			input: "Web-01.Example.COM.",
			want:  "web-01.example.com",
		},
		{
			name:  "digits only",
			input: "01",
			want:  "01",
		},
		{
			name:  "exactly the maximum length is kept as is",
			input: strings.Repeat("a", hostname.MaxLength),
			want:  strings.Repeat("a", hostname.MaxLength),
		},
		{
			name:  "one over the maximum length loses its last character",
			input: strings.Repeat("a", hostname.MaxLength+1),
			want:  strings.Repeat("a", hostname.MaxLength),
		},
		{
			name:  "uppercase and underscores are normalized before the cut",
			input: strings.Repeat("WEB_", 20) + "END",
			want:  strings.Repeat("web-", 13) + "web",
		},
		{
			name:  "a hyphen left at the end by the cut is removed",
			input: strings.Repeat("a", 54) + "-bbbb",
			want:  strings.Repeat("a", 54),
		},
		{
			name:  "a dot left at the end by the cut is removed",
			input: strings.Repeat("a", 54) + ".bbbb",
			want:  strings.Repeat("a", 54),
		},

		// Rejection cases
		{
			name:  "empty input",
			input: "",
		},
		{
			name:  "only a dot",
			input: ".",
		},
		{
			name:  "only whitespace",
			input: "   ",
		},
		{
			name:  "embedded space",
			input: "web 01",
		},
		{
			name:  "character normalization cannot fix",
			input: "web$01",
		},
		{
			name:  "leading hyphen",
			input: "-web01",
		},
		{
			name:  "leading underscore becomes a leading hyphen",
			input: "_web01",
		},
		{
			name:  "empty label between dots",
			input: "web..01",
		},
		{
			name:  "one character over the length limit",
			input: strings.Repeat("a", 254),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := hostname.Normalize(tc.input)

			if tc.want != "" {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)

				return
			}

			require.Error(t, err)
			assert.Empty(t, got)
			assert.Contains(t, err.Error(), tc.input)
		})
	}
}
