// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

// NewDUHTTPClient returns the HTTP client byohctl uses to talk to the management plane.
//
// When insecure is false the client is left with a nil Transport, so it behaves exactly
// as it did before --insecure existed: http.DefaultTransport, system trust store. When
// insecure is true, certificate verification is disabled -- see the --insecure flag help
// for why an operator would want that, and what it costs them.
func NewDUHTTPClient(timeout time.Duration, insecure bool) *http.Client {
	client := &http.Client{Timeout: timeout}
	if !insecure {
		return client
	}
	client.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		// #nosec G402 -- opted into explicitly via --insecure, for on-prem DUs serving a
		// self-signed or private-CA certificate.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return client
}

// MakeKubeconfigInsecure rewrites a kubeconfig so every cluster in it skips TLS
// verification, returning the re-serialized result.
//
// This exists because --insecure has to outlive the byohctl process. The host agent reads
// the kubeconfig byohctl leaves behind and has no TLS flags of its own (see agent/main.go),
// so unless the setting is recorded in the file, onboarding succeeds and the agent then
// fails against the very certificate the operator just told us to tolerate. The agent
// carries it forward through its bootstrap-token-to-certificate exchange, which copies
// Insecure into the kubeconfig it generates (agent/registration/csr.go).
//
// The certificate authority is cleared rather than left in place: client-go refuses a
// cluster that specifies both a CA and insecure-skip-tls-verify.
func MakeKubeconfigInsecure(kubeconfig []byte) ([]byte, error) {
	cfg, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	for _, cluster := range cfg.Clusters {
		cluster.InsecureSkipTLSVerify = true
		cluster.CertificateAuthority = ""
		cluster.CertificateAuthorityData = nil
	}

	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize kubeconfig: %w", err)
	}
	return out, nil
}
