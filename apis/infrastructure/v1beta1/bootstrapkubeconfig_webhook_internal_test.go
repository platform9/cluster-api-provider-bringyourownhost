// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	b64 "encoding/base64"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const wantAPIServerFormatDetail = "APIServer is not of the format https://hostname:port"

func TestValidateAPIServer(t *testing.T) {
	tests := []struct {
		name       string
		apiServer  string
		wantDetail string
	}{
		{"empty", "", "APIServer field cannot be empty"},
		{"invalid URL", "htt p://test.com", "APIServer URL is not valid"},
		{"missing scheme", "abc.com", wantAPIServerFormatDetail},
		{"missing hostname", "https://test-server", wantAPIServerFormatDetail},
		{"missing port", "https://test.com", wantAPIServerFormatDetail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{APIServer: tt.apiServer}}

			err := r.validateAPIServer()
			require.Error(t, err)

			var fieldErr *field.Error
			require.True(t, errors.As(err, &fieldErr), "expected a *field.Error, got %T", err)
			assert.Equal(t, field.ErrorTypeInvalid, fieldErr.Type)
			assert.Equal(t, "spec.apiserver", fieldErr.Field)
			assert.Equal(t, tt.apiServer, fieldErr.BadValue)
			assert.Equal(t, tt.wantDetail, fieldErr.Detail)
		})
	}

	t.Run("valid", func(t *testing.T) {
		r := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{APIServer: "https://abc.com:1234"}}
		assert.NoError(t, r.validateAPIServer())
	})
}

func TestValidateCAData(t *testing.T) {
	invalidCAData := "test-ca-data"
	nonPEMData := b64.StdEncoding.EncodeToString([]byte(invalidCAData))

	tests := []struct {
		name       string
		caData     string
		wantDetail string
	}{
		{"empty", "", "CertificateAuthorityData field cannot be empty"},
		{"not base64", invalidCAData, "cannot base64 decode CertificateAuthorityData"},
		{"not PEM encoded", nonPEMData, "CertificateAuthorityData is not PEM encoded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{CertificateAuthorityData: tt.caData}}

			err := r.validateCAData()
			require.Error(t, err)

			var fieldErr *field.Error
			require.True(t, errors.As(err, &fieldErr), "expected a *field.Error, got %T", err)
			assert.Equal(t, field.ErrorTypeInvalid, fieldErr.Type)
			assert.Equal(t, "spec.caData", fieldErr.Field)
			assert.Equal(t, tt.caData, fieldErr.BadValue)
			assert.Equal(t, tt.wantDetail, fieldErr.Detail)
		})
	}
}

func TestValidateCreateUpdateDelete(t *testing.T) {
	valid := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{
		APIServer:                "https://abc.com:1234",
		CertificateAuthorityData: b64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----")),
	}}
	invalid := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{APIServer: ""}}

	require.NoError(t, valid.ValidateCreate())
	require.Error(t, invalid.ValidateCreate())

	require.NoError(t, valid.ValidateUpdate(invalid))
	require.Error(t, invalid.ValidateUpdate(valid))

	require.NoError(t, valid.ValidateDelete())
	require.NoError(t, invalid.ValidateDelete())
}
