// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// testCADataValid is a structurally valid PEM block (base64-encoded, as the
// spec field requires). validateCAData only checks that the field decodes
// and PEM-parses; it does not verify the certificate itself.
var testCADataValid = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{
	Type:  "CERTIFICATE",
	Bytes: []byte("test-certificate-bytes"),
}))

const (
	testRequester = "admin@example.com"
	testHostName  = "example-worker-1"
)

func defaultingContext(t *testing.T, username string) context.Context {
	t.Helper()

	return admission.NewContextWithRequest(t.Context(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			UserInfo:  authenticationv1.UserInfo{Username: username},
		},
	})
}

func TestBootstrapKubeconfigDefaulter_CreatedBy(t *testing.T) {
	testCases := []struct {
		name     string
		supplied string
	}{
		{
			name:     "empty createdBy is filled from the requester",
			supplied: "",
		},
		{
			name:     "a body-supplied createdBy is overwritten, so a caller cannot claim another identity",
			supplied: "someone-else@example.com",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &BootstrapKubeconfig{
				Spec: BootstrapKubeconfigSpec{
					HostName:  testHostName,
					CreatedBy: tc.supplied,
				},
			}

			d := &BootstrapKubeconfigDefaulter{}
			err := d.Default(defaultingContext(t, testRequester), obj)
			require.NoError(t, err)

			assert.Equal(t, testRequester, obj.Spec.CreatedBy)
			// The expiry is written by the controller, so the webhook must leave it alone.
			assert.Nil(t, obj.Status.TokenExpiresAt)
		})
	}
}

func TestBootstrapKubeconfigDefaulter_Default_NoAdmissionContext(t *testing.T) {
	obj := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{HostName: testHostName}}

	d := &BootstrapKubeconfigDefaulter{}
	err := d.Default(t.Context(), obj)

	require.Error(t, err)
	assert.Empty(t, obj.Spec.CreatedBy)
}

func TestBootstrapKubeconfigDefaulter_Default_WrongType(t *testing.T) {
	d := &BootstrapKubeconfigDefaulter{}
	err := d.Default(defaultingContext(t, testRequester), &ByoHost{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a BootstrapKubeconfig")
}

func TestBootstrapKubeconfig_ValidateCreateUpdateErrors(t *testing.T) {
	validObj := func() *BootstrapKubeconfig {
		return &BootstrapKubeconfig{
			Spec: BootstrapKubeconfigSpec{
				APIServer:                "https://abc.com:1234",
				CertificateAuthorityData: testCADataValid,
				HostName:                 testHostName,
			},
		}
	}

	testCases := []struct {
		name     string
		oldObj   runtime.Object // nil runs ValidateCreate, otherwise ValidateUpdate
		hostName string
		wantErr  string
	}{
		{
			name:     "create with an empty host name is rejected",
			hostName: "",
			wantErr:  "cannot be empty",
		},
		{
			name:     "update from an object that is not a BootstrapKubeconfig is rejected",
			oldObj:   &ByoHost{},
			hostName: testHostName,
			wantErr:  "expected a BootstrapKubeconfig",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			obj := validObj()
			obj.Spec.HostName = tc.hostName

			var err error
			if tc.oldObj == nil {
				_, err = obj.ValidateCreate(t.Context(), obj)
			} else {
				_, err = obj.ValidateUpdate(t.Context(), tc.oldObj, obj)
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestBootstrapKubeconfig_validateHostName(t *testing.T) {
	testCases := []struct {
		name     string
		hostName string
		wantErr  string
	}{
		{
			name:     "a normalized host name is accepted",
			hostName: "example-worker-1",
		},
		{
			name:     "an empty host name is rejected",
			hostName: "",
			wantErr:  "cannot be empty",
		},
		{
			name:     "an unnormalized host name is rejected rather than rewritten",
			hostName: "Example_Worker_1",
			wantErr:  "not normalized",
		},
		{
			name:     "a host name that cannot normalize at all is rejected",
			hostName: "example worker 1",
			wantErr:  "not a valid object name",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{HostName: tc.hostName}}

			err := obj.validateHostName()

			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
