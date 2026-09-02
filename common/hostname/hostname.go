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

// Name is a host name that has already been through Normalize. Taking this
// type rather than a string means a caller cannot pass a raw host name where
// the normalized object name is required.
type Name string

// Normalize turns a machine's host name into the object name used for that
// host: lowercase, underscores replaced with hyphens, and a trailing dot
// removed. Kubernetes object names must be lowercase, and kubelet lowercases
// the node name it registers with, so the lowercasing matches both.
//
// Every consumer of a host name must call this, so that a host reaching the
// management cluster through byohctl, through the controller, or through the
// UI resolves to the same object name in all three.
//
// The normalized name can differ from the name the machine reports for
// itself. That is safe because kubelet is started with --node-name set to the
// normalized name, so the node object agrees with the ByoHost object. Nothing
// compares the machine's own hostname against either.
//
// An input that cannot be made into a valid RFC 1123 subdomain returns an
// error rather than being mangled further. Two different host names can
// normalize to the same object name; resolving that collision is not this
// function's job.
func Normalize(name string) (Name, error) {
	normalized := strings.ToLower(name)
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.TrimSuffix(normalized, ".")

	if errs := validation.IsDNS1123Subdomain(normalized); len(errs) > 0 {
		return "", fmt.Errorf("host name %q normalizes to %q, which is not a valid object name: %s",
			name, normalized, strings.Join(errs, "; "))
	}

	return Name(normalized), nil
}
