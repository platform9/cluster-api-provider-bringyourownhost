// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: nolintlint,testpackage
package registration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restclient "k8s.io/client-go/rest"
)

const testIdentity = "byoh:host:example-tenant:example-worker-1-abc12"

func newCertPEM(t *testing.T, commonName string) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestIdentityFromRESTConfig(t *testing.T) {
	t.Parallel()

	certPEM := newCertPEM(t, testIdentity)
	certFile := filepath.Join(t.TempDir(), "client.crt")
	err := os.WriteFile(certFile, certPEM, 0o600)
	require.NoError(t, err)

	testCases := []struct {
		name    string
		config  *restclient.Config
		want    string
		wantErr string
	}{
		{
			name: "common name from CertData",
			config: &restclient.Config{
				TLSClientConfig: restclient.TLSClientConfig{CertData: certPEM},
			},
			want: testIdentity,
		},
		{
			name: "common name from CertFile",
			config: &restclient.Config{
				TLSClientConfig: restclient.TLSClientConfig{CertFile: certFile},
			},
			want: testIdentity,
		},
		{
			name:   "no client certificate gives an empty identity",
			config: &restclient.Config{},
		},
		{
			name: "missing CertFile is an error",
			config: &restclient.Config{
				TLSClientConfig: restclient.TLSClientConfig{CertFile: filepath.Join(t.TempDir(), "absent.crt")},
			},
			wantErr: "read client certificate",
		},
		{
			name: "CertData that is not PEM is an error",
			config: &restclient.Config{
				TLSClientConfig: restclient.TLSClientConfig{CertData: []byte("not a certificate")},
			},
			wantErr: "no PEM certificate block",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := IdentityFromRESTConfig(tc.config)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveIdentity(t *testing.T) {
	t.Parallel()

	stateIdentity := "byoh:host:example-tenant:example-worker-1-stat2"
	certIdentity := "byoh:host:example-tenant:example-worker-1-cert2"
	certConfig := &restclient.Config{
		TLSClientConfig: restclient.TLSClientConfig{CertData: newCertPEM(t, certIdentity)},
	}

	testCases := []struct {
		name      string
		stateFile *State
		config    *restclient.Config
		want      string
		wantErr   error
	}{
		{
			name:      "state file wins over the client certificate",
			stateFile: &State{Version: stateVersion, Phase: PhasePending, Identity: stateIdentity},
			config:    certConfig,
			want:      stateIdentity,
		},
		{
			name:   "client certificate is used without a state file",
			config: certConfig,
			want:   certIdentity,
		},
		{
			name:      "client certificate is used when the state has no identity",
			stateFile: &State{Version: stateVersion, Phase: PhasePending},
			config:    certConfig,
			want:      certIdentity,
		},
		{
			name:    "neither source has an identity",
			config:  &restclient.Config{},
			wantErr: ErrNoIdentity,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tc.stateFile != nil {
				err := tc.stateFile.save(dir)
				require.NoError(t, err)
			}

			got, err := ResolveIdentity(dir, tc.config)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestByoHostNameFromIdentity(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		identity string
		want     string
		wantErr  bool
	}{
		{
			name:     "name is host name and suffix",
			identity: testIdentity,
			want:     "example-worker-1-abc12",
		},
		{
			name:     "identity without the suffix is an error",
			identity: "byoh:host:example-tenant:x",
			wantErr:  true,
		},
		{
			name:     "identity of another kind is an error",
			identity: "system:node:example",
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ByoHostNameFromIdentity(tc.identity)

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
