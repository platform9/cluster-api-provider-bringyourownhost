// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package identity defines the certificate identity of a host agent and
// converts it to and from the string used as the certificate's common name.
package identity

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Prefix is what every host certificate's common name starts with. The full
// form is "byoh:host:<tenantNamespace>:<normalizedHostName>-<suffix>".
const Prefix = "byoh:host:"

const (
	// SuffixLength is the length of the random suffix that makes each issued
	// certificate a distinct identity.
	SuffixLength = 5

	// segments is the number of ":" separated segments in a host identity:
	// "byoh", "host", the tenant namespace and the object name.
	segments = 4
	// tailLength is the "-" plus the suffix at the end of the object name.
	tailLength = SuffixLength + 1
)

// HostIdentity is the identity a host agent authenticates with. It names the
// tenant the host belongs to, the host, and the issuance of its certificate.
type HostIdentity struct {
	// TenantNamespace is the namespace of the ByoHost.
	TenantNamespace string
	// NormalizedHostName is the normalized host name the ByoHost is named for.
	NormalizedHostName string
	// Suffix is the random suffix that is distinct for each issued certificate.
	Suffix string
}

// HasPrefix reports whether s starts with Prefix, which is true of every host
// identity, valid or not.
func HasPrefix(s string) bool {
	return strings.HasPrefix(s, Prefix)
}

// String returns the certificate identity, which is the common name of the
// host certificate.
func (h HostIdentity) String() string {
	return Prefix + h.TenantNamespace + ":" + h.ByoHostName()
}

// ByoHostName returns the name of the ByoHost object for this identity.
func (h HostIdentity) ByoHostName() string {
	return h.NormalizedHostName + "-" + h.Suffix
}

// HostIdentityError is returned by Parse for an identity that is not valid.
// Problems holds every rule the identity broke, so a caller can inspect them
// with errors.Is and errors.As.
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

// Parse splits a certificate identity into its tenant namespace, normalized
// host name and suffix.
//
// The object name is split from the right: host names contain hyphens, so only
// the last "-" followed by five characters in [a-z0-9] is unambiguous. Segments
// are compared whole by the callers, because a substring match would let a
// certificate for "worker-1" act on "worker-10".
//
// An identity that breaks several rules returns one *HostIdentityError that
// lists all of them. When the identity is too malformed to split into a tenant
// namespace and a name, only the rule that stopped the split is listed.
func Parse(identity string) (HostIdentity, error) {
	if !HasPrefix(identity) {
		return HostIdentity{}, &HostIdentityError{
			Identity: identity,
			Problems: []error{fmt.Errorf("does not start with %q", Prefix)},
		}
	}

	parts := strings.Split(identity, ":")
	if len(parts) != segments {
		return HostIdentity{}, &HostIdentityError{
			Identity: identity,
			Problems: []error{fmt.Errorf("is not of the form %s<tenant>:<hostName>-<suffix>", Prefix)},
		}
	}

	var problems []error
	parsed := HostIdentity{TenantNamespace: parts[2]}

	if tenantProblems := validation.IsDNS1123Label(parsed.TenantNamespace); len(tenantProblems) > 0 {
		problems = append(problems, fmt.Errorf("has an invalid tenant %q: %s", parsed.TenantNamespace, strings.Join(tenantProblems, ", ")))
	}

	name := parts[3]
	switch {
	case name == "":
		problems = append(problems, errors.New("has an empty name"))
	case len(name) < tailLength:
		problems = append(problems, fmt.Errorf("has a name %q too short to end in -<suffix>", name))
	case name[len(name)-tailLength] != '-':
		problems = append(problems, fmt.Errorf("has a name %q with no hyphen before the %d character suffix", name, SuffixLength))
	default:
		parsed.NormalizedHostName = name[:len(name)-tailLength]
		parsed.Suffix = name[len(parsed.NormalizedHostName)+1:]

		if !isLowerAlphanumeric(parsed.Suffix) {
			problems = append(problems, fmt.Errorf("has an invalid suffix %q: want %d characters in [a-z0-9]", parsed.Suffix, SuffixLength))
		}

		if parsed.NormalizedHostName == "" {
			problems = append(problems, errors.New("has an empty host name"))
		} else if hostProblems := validation.IsDNS1123Subdomain(parsed.NormalizedHostName); len(hostProblems) > 0 {
			problems = append(problems, fmt.Errorf("has an invalid host name %q: %s", parsed.NormalizedHostName, strings.Join(hostProblems, ", ")))
		}

		// The object name must itself be a valid name, which bounds the host
		// name to what leaves room for the suffix.
		if nameProblems := validation.IsDNS1123Subdomain(name); len(nameProblems) > 0 {
			problems = append(problems, fmt.Errorf("has an invalid name %q: %s", name, strings.Join(nameProblems, ", ")))
		}
	}

	if len(problems) > 0 {
		return HostIdentity{}, &HostIdentityError{Identity: identity, Problems: problems}
	}

	return parsed, nil
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
