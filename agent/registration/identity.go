// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	restclient "k8s.io/client-go/rest"
)

// ErrNoIdentity means neither the state file nor a client certificate holds an
// identity.
var ErrNoIdentity = errors.New("no identity in the state file or in a client certificate")

// IdentityFromRESTConfig returns the common name of the client certificate in
// config, which is the identity the API server knows the agent by. It returns
// an empty string when the config has no client certificate.
func IdentityFromRESTConfig(config *restclient.Config) (string, error) {
	certData := config.CertData
	if len(certData) == 0 && config.CertFile != "" {
		var err error
		certData, err = os.ReadFile(config.CertFile)
		if err != nil {
			return "", fmt.Errorf("read client certificate %q: %w", config.CertFile, err)
		}
	}
	if len(certData) == 0 {
		return "", nil
	}

	block, _ := pem.Decode(certData)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("client certificate has no PEM certificate block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse client certificate: %w", err)
	}

	return cert.Subject.CommonName, nil
}

// ResolveIdentity returns the identity of this agent's onboarding. The state
// file next to the kubeconfig is read first; without a usable identity there,
// the common name of the client certificate in config is used. It returns
// ErrNoIdentity when neither has an identity.
func ResolveIdentity(stateDir string, config *restclient.Config) (string, error) {
	if st, err := loadState(stateDir); err == nil && st.Identity != "" {
		return st.Identity, nil
	}

	identity, err := IdentityFromRESTConfig(config)
	if err != nil {
		return "", err
	}
	if identity == "" {
		return "", fmt.Errorf("state directory %q: %w", filepath.Clean(stateDir), ErrNoIdentity)
	}

	return identity, nil
}

// ByoHostNameFromIdentity returns the name of the ByoHost that belongs to a
// host certificate identity.
func ByoHostNameFromIdentity(identity string) (string, error) {
	_, hostName, suffix, err := infrastructurev1beta1.ParseHostIdentity(identity)
	if err != nil {
		return "", fmt.Errorf("parse agent identity: %w", err)
	}

	return infrastructurev1beta1.ByoHostName(hostName, suffix), nil
}
