// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"fmt"
	"strings"
)

// FIXME CLAUDE: Move this file's contents into byohost_webhook.go. Similar for the tests.

// FIXME CLAUDE: Make all public declarations private if they're not used outside the package.

// HostIdentityPrefix is what every host certificate's common name starts
// with. The two segments after it are the host name and the suffix of the
// BootstrapKubeconfig that issued the certificate.
const HostIdentityPrefix = "byoh:host:"

// hostIdentitySegments is how many colon-separated segments a host identity
// has: "byoh", "host", the host name, and the issuance suffix.
const hostIdentitySegments = 4

// FIXME CLAUDE: Is this even used anywhere except tests?
// HostIdentity builds the certificate common name for one issuance. The
// suffix makes every issuance for a host a distinct identity, so a
// certificate from an earlier onboarding cannot act as the host after it is
// onboarded again.
func HostIdentity(hostName, suffix string) string {
	return fmt.Sprintf("%s%s:%s", HostIdentityPrefix, hostName, suffix)
}

// HostNameFromIdentity returns the host name a certificate identity names.
//
// FIXME CLAUDE: This comment is implementation detail reasoning that does not belong in the function's doc. Instead it should be in the call site or near the component that applies this behaviour
// The host name is carried in the identity itself so that nothing has to look
// up the object that issued the certificate, which may be long gone by the
// time a check needs the name.
//
// The check is performed on the entire segments because a substring match would
// let a certificate for "worker-1" act on "worker-10".
//
// FIXME CLAUDE: Add an example of what input to output is returned in the happy path. Single example only.
func HostNameFromIdentity(identity string) (string, error) {
	if !strings.HasPrefix(identity, HostIdentityPrefix) {
		return "", fmt.Errorf("identity %q does not start with %q", identity, HostIdentityPrefix)
	}

	segments := strings.Split(identity, ":")
	// FIXME CLAUDE: single use const hostIdentitySegments. Replace with value
	// directly.
	if len(segments) != hostIdentitySegments {
		return "", fmt.Errorf("identity %q is not of the form %s<hostName>:<suffix>", identity, HostIdentityPrefix)
	}

	hostName := segments[2]
	if hostName == "" {
		return "", fmt.Errorf("identity %q carries an empty host name", identity)
	}

	if segments[3] == "" {
		return "", fmt.Errorf("identity %q carries an empty suffix", identity)
	}

	return hostName, nil
}
