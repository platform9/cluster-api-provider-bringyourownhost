package service

import (
	"testing"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/version"
)

// TestByohAgentBundleURL locks in that the bundle URL is a straight
// string composition of the repo path and byohctl's own baked-in
// version -- including the "wrong" cases (dirty suffix, unset-version
// default). Those aren't bugs to special-case: a bundle tag that
// doesn't exist in quay must fail the subsequent imgpkg pull loudly,
// not silently resolve to some other bundle.
func TestByohAgentBundleURL(t *testing.T) {
	testCases := []struct {
		name        string
		versionOvrd string
		want        string
	}{
		{
			name:        "version baked in via ldflags",
			versionOvrd: "v1.2.3-4-gabcdef0",
			want:        "quay.io/platform9/cluster-api-provider-bringyourownhost/agent:v1.2.3-4-gabcdef0",
		},
		{
			name:        "dirty working tree suffix passes through verbatim",
			versionOvrd: "v1.2.3-4-gabcdef0-dirty",
			want:        "quay.io/platform9/cluster-api-provider-bringyourownhost/agent:v1.2.3-4-gabcdef0-dirty",
		},
		{
			name:        "unset version falls back to GetVersion's 0.0.0 default",
			versionOvrd: "",
			want:        "quay.io/platform9/cluster-api-provider-bringyourownhost/agent:0.0.0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// version.Version is a package-level global; save/restore so
			// this test doesn't bleed into others. Not run with
			// t.Parallel() for the same reason.
			original := version.Version
			defer func() { version.Version = original }()

			version.Version = tc.versionOvrd

			got := ByohAgentBundleURL()
			if got != tc.want {
				t.Errorf("ByohAgentBundleURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
