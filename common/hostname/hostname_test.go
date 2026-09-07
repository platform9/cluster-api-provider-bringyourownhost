// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package hostname_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
)

func TestNormalize(t *testing.T) {
	testCases := []struct {
		name    string
		input   string
		want    hostname.Name
		wantErr bool
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
			name:  "longest accepted name",
			input: strings.Repeat("a", 253),
			want:  hostname.Name(strings.Repeat("a", 253)),
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "only a dot",
			input:   ".",
			wantErr: true,
		},
		{
			name:    "only whitespace",
			input:   "   ",
			wantErr: true,
		},
		{
			name:    "embedded space",
			input:   "web 01",
			wantErr: true,
		},
		{
			name:    "character normalization cannot fix",
			input:   "web$01",
			wantErr: true,
		},
		{
			name:    "leading hyphen",
			input:   "-web01",
			wantErr: true,
		},
		{
			name:    "leading underscore becomes a leading hyphen",
			input:   "_web01",
			wantErr: true,
		},
		{
			name:    "empty label between dots",
			input:   "web..01",
			wantErr: true,
		},
		{
			name:    "one character over the length limit",
			input:   strings.Repeat("a", 254),
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := hostname.Normalize(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, got)
				assert.Contains(t, err.Error(), tc.input)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	t.Parallel()

	first, err := hostname.Normalize("Web_01.Example.COM.")
	require.NoError(t, err)

	second, err := hostname.Normalize(string(first))
	require.NoError(t, err)
	assert.Equal(t, first, second)
}
