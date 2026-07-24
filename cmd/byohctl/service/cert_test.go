// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// splitTestServerHostPort returns the host and port of an httptest server URL.
func splitTestServerHostPort(t *testing.T, rawURL string) (string, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse test server URL %q: %v", rawURL, err)
	}
	return u.Hostname(), u.Port()
}

func TestFetchServerCertChain(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()

	host, port := splitTestServerHostPort(t, ts.URL)

	oldPort := caCertPort
	caCertPort = port
	defer func() { caCertPort = oldPort }()

	pemBytes, err := fetchServerCertChain(host)
	if err != nil {
		t.Fatalf("fetchServerCertChain returned error: %v", err)
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("expected a CERTIFICATE PEM block, got %v", block)
	}

	fetched, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse fetched certificate: %v", err)
	}
	if !fetched.Equal(ts.Certificate()) {
		t.Errorf("fetched certificate does not match the server's certificate")
	}
}

func TestInstallCACert(t *testing.T) {
	tempDir := t.TempDir()

	oldDir := CACertDir
	CACertDir = tempDir
	defer func() { CACertDir = oldDir }()

	commandRun := false
	oldExec := execCommand
	execCommand = func(command string, args ...string) *exec.Cmd {
		if command == UpdateCACertsCmd {
			commandRun = true
		}
		return mockCommand(command)
	}
	defer func() { execCommand = oldExec }()

	caPEM := []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")
	if err := installCACert("test.platform9.local", caPEM); err != nil {
		t.Fatalf("installCACert returned error: %v", err)
	}

	if !commandRun {
		t.Errorf("expected %s to be invoked", UpdateCACertsCmd)
	}

	certPath := filepath.Join(tempDir, "pf9-test.platform9.local.crt")
	content, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("expected CA cert written to %s: %v", certPath, err)
	}
	if string(content) != string(caPEM) {
		t.Errorf("written CA content mismatch: got %q", string(content))
	}
}

func TestEnsureCATrustAlreadyTrusted(t *testing.T) {
	tempDir := t.TempDir()
	oldDir := CACertDir
	CACertDir = tempDir
	defer func() { CACertDir = oldDir }()

	oldProbe := tlsProbe
	tlsProbe = func(address, serverName string) error { return nil }
	defer func() { tlsProbe = oldProbe }()

	pool, err := EnsureCATrust("trusted.platform9.local", "")
	if err != nil {
		t.Fatalf("EnsureCATrust returned error: %v", err)
	}
	if pool != nil {
		t.Errorf("expected nil pool when CA already trusted, got non-nil")
	}

	entries, _ := os.ReadDir(tempDir)
	if len(entries) != 0 {
		t.Errorf("expected no files written when already trusted, found %d", len(entries))
	}
}

func TestEnsureCATrustNonAuthorityError(t *testing.T) {
	tempDir := t.TempDir()
	oldDir := CACertDir
	CACertDir = tempDir
	defer func() { CACertDir = oldDir }()

	oldProbe := tlsProbe
	tlsProbe = func(address, serverName string) error { return errors.New("connection refused") }
	defer func() { tlsProbe = oldProbe }()

	pool, err := EnsureCATrust("unreachable.platform9.local", "")
	if err == nil {
		t.Fatalf("expected error for non-authority failure, got nil")
	}
	if pool != nil {
		t.Errorf("expected nil pool on error")
	}

	entries, _ := os.ReadDir(tempDir)
	if len(entries) != 0 {
		t.Errorf("expected no files written on non-authority error, found %d", len(entries))
	}
}

func TestEnsureCATrustInstallsAndReturnsPool(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()

	host, port := splitTestServerHostPort(t, ts.URL)

	oldPort := caCertPort
	caCertPort = port
	defer func() { caCertPort = oldPort }()

	tempDir := t.TempDir()
	oldDir := CACertDir
	CACertDir = tempDir
	defer func() { CACertDir = oldDir }()

	oldExec := execCommand
	execCommand = func(command string, args ...string) *exec.Cmd { return mockCommand(command) }
	defer func() { execCommand = oldExec }()

	// Default tlsProbe against the untrusted test server yields an unknown-authority error,
	// which is the path we want to exercise.
	pool, err := EnsureCATrust(host, "")
	if err != nil {
		t.Fatalf("EnsureCATrust returned error: %v", err)
	}
	if pool == nil {
		t.Fatalf("expected a non-nil cert pool")
	}

	// The CA must have been installed to the (overridden) system anchor dir.
	certPath := filepath.Join(tempDir, "pf9-"+host+".crt")
	if _, statErr := os.Stat(certPath); statErr != nil {
		t.Errorf("expected CA installed at %s: %v", certPath, statErr)
	}

	// The returned pool must now trust the server.
	conn, err := tls.Dial("tcp", ts.Listener.Addr().String(), &tls.Config{
		RootCAs:    pool,
		ServerName: host,
	})
	if err != nil {
		t.Fatalf("expected returned pool to trust the server, dial failed: %v", err)
	}
	conn.Close()
}
