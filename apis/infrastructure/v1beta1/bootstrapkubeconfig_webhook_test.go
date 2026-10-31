// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestBootstrapKubeconfig_ValidateOptionalAPIServerAndCA(t *testing.T) {
	apiServerPath := field.NewPath("spec").Child("apiserver")
	caDataPath := field.NewPath("spec").Child("caData")
	notPEMCAData := base64.StdEncoding.EncodeToString([]byte("not-a-pem-block"))

	testCases := []struct {
		name      string
		update    bool
		apiServer string
		caData    string
		wantErr   error
	}{
		{
			name:      "create with empty apiserver and CA is admitted",
			update:    false,
			apiServer: "",
			caData:    "",
			wantErr:   nil,
		},
		{
			name:      "create with a valid apiserver and CA is admitted",
			update:    false,
			apiServer: "https://abc.com:1234",
			caData:    testCADataValid,
			wantErr:   nil,
		},
		{
			name:      "create with a malformed apiserver URL is rejected",
			update:    false,
			apiServer: "htt p://test.com",
			caData:    "",
			wantErr:   field.Invalid(apiServerPath, "htt p://test.com", "APIServer URL is not valid"),
		},
		{
			name:      "create with an apiserver that has no port is rejected",
			update:    false,
			apiServer: "https://test.com",
			caData:    "",
			wantErr:   field.Invalid(apiServerPath, "https://test.com", "APIServer is not of the format https://hostname:port"),
		},
		{
			name:      "create with CA data that is not base64 is rejected",
			update:    false,
			apiServer: "https://abc.com:1234",
			caData:    "test-ca-data",
			wantErr:   field.Invalid(caDataPath, "test-ca-data", "cannot base64 decode CertificateAuthorityData"),
		},
		{
			name:      "create with CA data that is not PEM is rejected",
			update:    false,
			apiServer: "https://abc.com:1234",
			caData:    notPEMCAData,
			wantErr:   field.Invalid(caDataPath, notPEMCAData, "CertificateAuthorityData is not PEM encoded"),
		},
		{
			name:      "update with empty apiserver and CA is admitted",
			update:    true,
			apiServer: "",
			caData:    "",
			wantErr:   nil,
		},
		{
			name:      "update with a malformed apiserver URL is rejected",
			update:    true,
			apiServer: "htt p://test.com",
			caData:    "",
			wantErr:   field.Invalid(apiServerPath, "htt p://test.com", "APIServer URL is not valid"),
		},
		{
			name:      "update with CA data that is not PEM is rejected",
			update:    true,
			apiServer: "https://abc.com:1234",
			caData:    notPEMCAData,
			wantErr:   field.Invalid(caDataPath, notPEMCAData, "CertificateAuthorityData is not PEM encoded"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &BootstrapKubeconfig{
				Spec: BootstrapKubeconfigSpec{
					APIServer:                tc.apiServer,
					CertificateAuthorityData: tc.caData,
					HostName:                 testHostName,
				},
			}

			var err error
			if tc.update {
				_, err = obj.ValidateUpdate(t.Context(), &BootstrapKubeconfig{}, obj)
			} else {
				_, err = obj.ValidateCreate(t.Context(), obj)
			}

			assert.Equal(t, tc.wantErr, err)
		})
	}
}
