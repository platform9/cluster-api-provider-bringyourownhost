// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package hostname maps a machine's reported host name onto the object name
// used for it in the management cluster.
package hostname

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// FIXME CLAUDE: Placeholder for future refactor. Maybe hostname.Name is the
// type that everyone uses. And calls hostname.New("string") -> hostname.Name.
// So Normalize is a side effect instead.
type Name struct {
	val string
}

func (n *Name) String() string {
	return n.val
}

// Normalize turns a machine's host name into the object name used for that
// host:
// - lowercase
// - underscores replaced with hyphens
// - trailing dot removed
//
// It ensures that the returned value can be used as a valid RFC 1123 subdomain
// or returns an error.
//
// CAVEAT: Two separate hostname inputs may resolve to the same normalized
// output. But ensuring unique hostnames is beyone the scope of this function.
//
// NOTE: Ensure kubeadm's nodeRegistration.name is set to the normalized name,
// so the Node and the ByoHost agree.
func Normalize(name string) (string, error) {
	normalized := strings.ToLower(name)
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.TrimSuffix(normalized, ".")

	if errs := validation.IsDNS1123Subdomain(normalized); len(errs) > 0 {
		return "", fmt.Errorf("host name %q normalizes to %q, which is not a valid object name: %s",
			name, normalized, strings.Join(errs, "; "))
	}

	return normalized, nil
}
