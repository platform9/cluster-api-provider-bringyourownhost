// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// HostIdentityPrefix is what every host certificate's common name starts with.
// The full form is "byoh:host:<tenant>:<hostName>-<suffix>".
const HostIdentityPrefix = "byoh:host:"

const (
	// hostIdentitySegments is the number of ":" separated segments in a host
	// identity: "byoh", "host", the tenant and the object name.
	hostIdentitySegments = 4
	// hostIdentitySuffixLen is the length of the random suffix that makes each
	// issued certificate a distinct identity.
	hostIdentitySuffixLen = 5
	// hostIdentityTailLen is the "-" plus the suffix at the end of the object name.
	hostIdentityTailLen = hostIdentitySuffixLen + 1
)

// ByoHostName returns the name of the ByoHost object for a host name and the
// suffix of its identity.
func ByoHostName(hostName, suffix string) string {
	return hostName + "-" + suffix
}

// FormatHostIdentity returns the certificate identity for a host in a tenant.
func FormatHostIdentity(tenant, hostName, suffix string) string {
	return HostIdentityPrefix + tenant + ":" + ByoHostName(hostName, suffix)
}

// ParseHostIdentity splits a certificate identity into its tenant, host name
// and suffix. The tenant is the namespace of the ByoHost.
//
// The object name is split from the right: host names contain hyphens, so only
// the last "-" followed by five characters in [a-z0-9] is unambiguous. Segments
// are compared whole by the callers, because a substring match would let a
// certificate for "worker-1" act on "worker-10".
func ParseHostIdentity(identity string) (tenant, hostName, suffix string, err error) {
	if !strings.HasPrefix(identity, HostIdentityPrefix) {
		return "", "", "", fmt.Errorf("identity %q does not start with %q", identity, HostIdentityPrefix)
	}

	segments := strings.Split(identity, ":")
	if len(segments) != hostIdentitySegments {
		return "", "", "", fmt.Errorf("identity %q is not of the form %s<tenant>:<hostName>-<suffix>", identity, HostIdentityPrefix)
	}

	tenant = segments[2]
	if problems := validation.IsDNS1123Label(tenant); len(problems) > 0 {
		return "", "", "", fmt.Errorf("identity %q has an invalid tenant %q: %s", identity, tenant, strings.Join(problems, "; "))
	}

	name := segments[3]
	if name == "" {
		return "", "", "", fmt.Errorf("identity %q has an empty name", identity)
	}

	if len(name) < hostIdentityTailLen {
		return "", "", "", fmt.Errorf("identity %q has a name %q too short to end in -<suffix>", identity, name)
	}

	hostName = name[:len(name)-hostIdentityTailLen]
	if name[len(hostName)] != '-' {
		return "", "", "", fmt.Errorf("identity %q has a name %q with no hyphen before the %d character suffix", identity, name, hostIdentitySuffixLen)
	}

	suffix = name[len(hostName)+1:]
	if !isLowerAlphanumeric(suffix) {
		return "", "", "", fmt.Errorf("identity %q has an invalid suffix %q: want %d characters in [a-z0-9]", identity, suffix, hostIdentitySuffixLen)
	}

	if hostName == "" {
		return "", "", "", fmt.Errorf("identity %q has an empty host name", identity)
	}

	if problems := validation.IsDNS1123Subdomain(hostName); len(problems) > 0 {
		return "", "", "", fmt.Errorf("identity %q has an invalid host name %q: %s", identity, hostName, strings.Join(problems, "; "))
	}

	// The object name must itself be a valid name, which bounds the host name
	// to what leaves room for the suffix.
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return "", "", "", fmt.Errorf("identity %q has an invalid name %q: %s", identity, name, strings.Join(problems, "; "))
	}

	return tenant, hostName, suffix, nil
}

// isLowerAlphanumeric reports whether s is non-empty and made only of the
// characters a-z and 0-9.
func isLowerAlphanumeric(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}

	return s != ""
}
