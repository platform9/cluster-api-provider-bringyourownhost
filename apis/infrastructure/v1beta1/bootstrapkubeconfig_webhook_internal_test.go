// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		})
	}
}

func TestBootstrapKubeconfigDefaulter_TokenExpiresAt(t *testing.T) {
	t.Run("an absent expiry is defaulted", func(t *testing.T) {
		obj := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{HostName: testHostName}}

		d := &BootstrapKubeconfigDefaulter{}
		err := d.Default(defaultingContext(t, testRequester), obj)
		require.NoError(t, err)

		require.NotNil(t, obj.Spec.TokenExpiresAt)
		assert.WithinDuration(t, time.Now().Add(DefaultTokenExpiry), obj.Spec.TokenExpiresAt.Time, time.Minute)
	})

	t.Run("a supplied expiry is discarded and replaced with the default window", func(t *testing.T) {
		supplied := metav1.NewTime(time.Now().Add(10 * time.Minute))
		obj := &BootstrapKubeconfig{
			Spec: BootstrapKubeconfigSpec{
				HostName:       testHostName,
				TokenExpiresAt: &supplied,
			},
		}

		d := &BootstrapKubeconfigDefaulter{}
		err := d.Default(defaultingContext(t, testRequester), obj)
		require.NoError(t, err)

		require.NotNil(t, obj.Spec.TokenExpiresAt)
		assert.NotEqual(t, supplied.Time, obj.Spec.TokenExpiresAt.Time)
		assert.WithinDuration(t, time.Now().Add(DefaultTokenExpiry), obj.Spec.TokenExpiresAt.Time, time.Minute)
	})

	t.Run("a request without admission context is an error, not a silent empty creator", func(t *testing.T) {
		obj := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{HostName: testHostName}}

		d := &BootstrapKubeconfigDefaulter{}
		err := d.Default(t.Context(), obj)
		require.Error(t, err)
		assert.Empty(t, obj.Spec.CreatedBy)
	})
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
		{
			name:     "update that changes the host name is rejected",
			oldObj:   validObj(),
			hostName: "example-worker-2",
			wantErr:  "hostName is immutable",
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

func TestValidateImmutableFields(t *testing.T) {
	expiry := metav1.NewTime(time.Now().Add(DefaultTokenExpiry))
	base := func() *BootstrapKubeconfig {
		return &BootstrapKubeconfig{
			Spec: BootstrapKubeconfigSpec{
				HostName:       testHostName,
				CreatedBy:      testRequester,
				TokenExpiresAt: &expiry,
			},
		}
	}

	testCases := []struct {
		name    string
		mutate  func(*BootstrapKubeconfig)
		wantErr string
	}{
		{
			name:   "an unchanged spec is accepted",
			mutate: func(_ *BootstrapKubeconfig) {},
		},
		{
			name:    "changing the host name is rejected",
			mutate:  func(o *BootstrapKubeconfig) { o.Spec.HostName = "example-worker-2" },
			wantErr: "hostName is immutable",
		},
		{
			name:    "changing the creator is rejected, so the credential grant cannot be redirected",
			mutate:  func(o *BootstrapKubeconfig) { o.Spec.CreatedBy = "someone-else@example.com" },
			wantErr: "createdBy is immutable",
		},
		{
			name: "extending the expiry is rejected, so a live token's life cannot be stretched",
			mutate: func(o *BootstrapKubeconfig) {
				later := metav1.NewTime(expiry.Add(time.Hour))
				o.Spec.TokenExpiresAt = &later
			},
			wantErr: "tokenExpiresAt is immutable",
		},
		{
			name:    "clearing the expiry is rejected",
			mutate:  func(o *BootstrapKubeconfig) { o.Spec.TokenExpiresAt = nil },
			wantErr: "tokenExpiresAt is immutable",
		},
		{
			name:    "changing an unrelated field is accepted",
			mutate:  func(o *BootstrapKubeconfig) { o.Spec.InsecureSkipTLSVerify = true },
			wantErr: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			oldObj := base()
			newObj := base()
			tc.mutate(newObj)

			err := validateImmutableFields(oldObj, newObj)

			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
