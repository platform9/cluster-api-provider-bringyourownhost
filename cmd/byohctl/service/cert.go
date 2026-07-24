// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package service contains BYOH agent setup functions.
package service

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

// caCertPort is the port probed/dialed for the management-plane TLS certificate.
// Declared as a var so tests can point it at an ephemeral test server.
var caCertPort = "443"

// tlsProbe reports whether the given address presents a certificate trusted by the default
// system trust store. It returns nil on success. Declared as a var so tests can simulate
// the trusted / other-error paths without a live endpoint.
var tlsProbe = func(address, serverName string) error {
	conn, err := tls.Dial("tcp", address, &tls.Config{ServerName: serverName})
	if err == nil {
		conn.Close()
	}
	return err
}

// caCertFilename returns the filename used to store the management-plane CA under
// CACertDir. It is derived from the FQDN so onboarding against different DUs does not
// clobber a previously installed CA.
func caCertFilename(fqdn string) string {
	return "pf9-" + fqdn + ".crt"
}

// EnsureCATrust makes sure the management-plane FQDN presents a TLS certificate that the
// host trusts. It first probes the FQDN with the default system trust store; if that
// already succeeds it returns (nil, nil) and makes no changes. Only when the probe fails
// with an "unknown authority" error does it obtain the CA (from caCertPath if provided,
// otherwise fetched from the server), install it into the Ubuntu system trust store, and
// return an in-process cert pool trusting that CA for the remainder of the run.
//
// The returned pool is needed in addition to the system-store install because Go caches
// system roots per-process for the default (nil RootCAs) path, so HTTP clients created
// after update-ca-certificates in the same process may not otherwise see the new CA.
func EnsureCATrust(fqdn, caCertPath string) (*x509.CertPool, error) {
	address := net.JoinHostPort(fqdn, caCertPort)

	// Probe using the default (system) trust store.
	err := tlsProbe(address, fqdn)
	if err == nil {
		utils.LogDebug("Management plane CA for %s is already trusted", fqdn)
		return nil, nil
	}

	// Only an unknown-authority failure is something we can fix by trusting a CA.
	// Hostname mismatches, dial failures, DNS errors, etc. are surfaced as-is.
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuthority) {
		return nil, fmt.Errorf("TLS connection to %s failed: %w", address, err)
	}

	utils.LogInfo("Management plane CA for %s is not trusted, establishing trust", fqdn)

	var caPEM []byte
	if caCertPath != "" {
		utils.LogInfo("Reading CA certificate from %s", caCertPath)
		caPEM, err = os.ReadFile(caCertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate file %s: %w", caCertPath, err)
		}
	} else {
		utils.LogInfo("Fetching CA certificate chain from %s", address)
		caPEM, err = fetchServerCertChain(fqdn)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch server certificate chain from %s: %w", address, err)
		}
	}

	if err := installCACert(fqdn, caPEM); err != nil {
		return nil, err
	}

	// Build an in-process pool so this run's clients trust the CA regardless of Go's
	// cached system roots.
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no valid certificates found in CA certificate for %s", fqdn)
	}

	return pool, nil
}

// fetchServerCertChain connects to fqdn:443 without verifying the certificate and returns
// the presented certificate chain PEM-encoded. This mirrors the manual
// `openssl s_client -showcerts` capture used as an onboarding pre-req.
func fetchServerCertChain(fqdn string) ([]byte, error) {
	address := net.JoinHostPort(fqdn, caCertPort)
	// #nosec G402 -- InsecureSkipVerify is intentional: we are fetching an as-yet-untrusted
	// private CA to install it (trust-on-first-use), not authenticating a session.
	conn, err := tls.Dial("tcp", address, &tls.Config{
		ServerName:         fqdn,
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, fmt.Errorf("server %s presented no certificates", address)
	}

	var buf []byte
	for _, cert := range certs {
		block := &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}
		buf = append(buf, pem.EncodeToMemory(block)...)
	}
	return buf, nil
}

// installCACert writes the CA PEM into the Ubuntu system anchor directory and refreshes
// the trust store with update-ca-certificates. It overwrites any existing file of the
// same name; since EnsureCATrust only calls it after a failed probe, it does not run when
// the CA is already trusted.
func installCACert(fqdn string, caPEM []byte) error {
	certPath := filepath.Join(CACertDir, caCertFilename(fqdn))

	if err := os.WriteFile(certPath, caPEM, DefaultFilePerms); err != nil {
		return fmt.Errorf("failed to write CA certificate to %s: %w", certPath, err)
	}
	utils.LogInfo("Installed CA certificate to %s", certPath)

	cmd := execCommand(UpdateCACertsCmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to run %s: %w: %s", UpdateCACertsCmd, err, string(output))
	}
	utils.LogSuccess("Updated system CA trust store")

	return nil
}
