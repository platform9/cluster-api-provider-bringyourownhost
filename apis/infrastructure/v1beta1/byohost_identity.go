// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"errors"
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

// HostIdentityError is returned by ParseHostIdentity for an identity that is
// not valid. Problems holds every rule the identity broke, so a caller can
// inspect them with errors.Is and errors.As.
//
// +kubebuilder:object:generate=false
type HostIdentityError struct {
	// Identity is the identity that failed to parse.
	Identity string
	// Problems are the rules the identity broke, in the order they were found.
	Problems []error
}

// Error implements the error interface.
func (e *HostIdentityError) Error() string {
	messages := make([]string, 0, len(e.Problems))
	for _, problem := range e.Problems {
		messages = append(messages, problem.Error())
	}

	return fmt.Sprintf("identity %q is invalid: %s", e.Identity, strings.Join(messages, "; "))
}

// Unwrap returns the problems so that errors.Is and errors.As match each one.
func (e *HostIdentityError) Unwrap() []error {
	return e.Problems
}

// ParseHostIdentity splits a certificate identity into its tenant, host name
// and suffix. The tenant is the namespace of the ByoHost.
//
// The object name is split from the right: host names contain hyphens, so only
// the last "-" followed by five characters in [a-z0-9] is unambiguous. Segments
// are compared whole by the callers, because a substring match would let a
// certificate for "worker-1" act on "worker-10".
//
// An identity that breaks several rules returns one *HostIdentityError that
// lists all of them. When the identity is too malformed to split into a tenant
// and a name, only the rule that stopped the split is listed.
func ParseHostIdentity(identity string) (tenant, hostName, suffix string, err error) {
	if !strings.HasPrefix(identity, HostIdentityPrefix) {
		return "", "", "", &HostIdentityError{
			Identity: identity,
			Problems: []error{fmt.Errorf("does not start with %q", HostIdentityPrefix)},
		}
	}

	segments := strings.Split(identity, ":")
	if len(segments) != hostIdentitySegments {
		return "", "", "", &HostIdentityError{
			Identity: identity,
			Problems: []error{fmt.Errorf("is not of the form %s<tenant>:<hostName>-<suffix>", HostIdentityPrefix)},
		}
	}

	var problems []error

	tenant = segments[2]
	if tenantProblems := validation.IsDNS1123Label(tenant); len(tenantProblems) > 0 {
		problems = append(problems, fmt.Errorf("has an invalid tenant %q: %s", tenant, strings.Join(tenantProblems, ", ")))
	}

	name := segments[3]
	switch {
	case name == "":
		problems = append(problems, errors.New("has an empty name"))
	case len(name) < hostIdentityTailLen:
		problems = append(problems, fmt.Errorf("has a name %q too short to end in -<suffix>", name))
	case name[len(name)-hostIdentityTailLen] != '-':
		problems = append(problems, fmt.Errorf("has a name %q with no hyphen before the %d character suffix", name, hostIdentitySuffixLen))
	default:
		hostName = name[:len(name)-hostIdentityTailLen]
		suffix = name[len(hostName)+1:]

		if !isLowerAlphanumeric(suffix) {
			problems = append(problems, fmt.Errorf("has an invalid suffix %q: want %d characters in [a-z0-9]", suffix, hostIdentitySuffixLen))
		}

		if hostName == "" {
			problems = append(problems, errors.New("has an empty host name"))
		} else if hostProblems := validation.IsDNS1123Subdomain(hostName); len(hostProblems) > 0 {
			problems = append(problems, fmt.Errorf("has an invalid host name %q: %s", hostName, strings.Join(hostProblems, ", ")))
		}

		// The object name must itself be a valid name, which bounds the host
		// name to what leaves room for the suffix.
		if nameProblems := validation.IsDNS1123Subdomain(name); len(nameProblems) > 0 {
			problems = append(problems, fmt.Errorf("has an invalid name %q: %s", name, strings.Join(nameProblems, ", ")))
		}
	}

	if len(problems) > 0 {
		return "", "", "", &HostIdentityError{Identity: identity, Problems: problems}
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
