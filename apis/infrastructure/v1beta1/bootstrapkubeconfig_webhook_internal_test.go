// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	testRequester = "admin@example.com"
	testHostName  = "coke-worker-1"
)

func defaultingContext(username string) context.Context {
	return admission.NewContextWithRequest(context.Background(), admission.Request{
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
			err := d.Default(defaultingContext(testRequester), obj)
			require.NoError(t, err)

			assert.Equal(t, testRequester, obj.Spec.CreatedBy)
		})
	}
}

func TestBootstrapKubeconfigDefaulter_TokenExpiresAt(t *testing.T) {
	t.Run("an absent expiry is defaulted", func(t *testing.T) {
		obj := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{HostName: testHostName}}

		d := &BootstrapKubeconfigDefaulter{}
		err := d.Default(defaultingContext(testRequester), obj)
		require.NoError(t, err)

		require.NotNil(t, obj.Spec.TokenExpiresAt)
		assert.WithinDuration(t, time.Now().Add(DefaultTokenExpiry), obj.Spec.TokenExpiresAt.Time, time.Minute)
	})

	t.Run("a supplied expiry is left alone, and the validator bounds it instead", func(t *testing.T) {
		supplied := metav1.NewTime(time.Now().Add(10 * time.Minute))
		obj := &BootstrapKubeconfig{
			Spec: BootstrapKubeconfigSpec{
				HostName:       testHostName,
				TokenExpiresAt: &supplied,
			},
		}

		d := &BootstrapKubeconfigDefaulter{}
		err := d.Default(defaultingContext(testRequester), obj)
		require.NoError(t, err)

		require.NotNil(t, obj.Spec.TokenExpiresAt)
		assert.Equal(t, supplied.Time, obj.Spec.TokenExpiresAt.Time)
	})

	t.Run("a request without admission context is an error, not a silent empty creator", func(t *testing.T) {
		obj := &BootstrapKubeconfig{Spec: BootstrapKubeconfigSpec{HostName: testHostName}}

		d := &BootstrapKubeconfigDefaulter{}
		err := d.Default(context.Background(), obj)
		require.Error(t, err)
		assert.Empty(t, obj.Spec.CreatedBy)
	})
}

func TestBootstrapKubeconfig_validateHostName(t *testing.T) {
	testCases := []struct {
		name     string
		hostName string
		wantErr  string
	}{
		{
			name:     "a normalized host name is accepted",
			hostName: "coke-worker-1",
		},
		{
			name:     "an empty host name is rejected",
			hostName: "",
			wantErr:  "cannot be empty",
		},
		{
			name:     "an unnormalized host name is rejected rather than rewritten",
			hostName: "Coke_Worker_1",
			wantErr:  "not normalized",
		},
		{
			name:     "a host name that cannot normalize at all is rejected",
			hostName: "coke worker 1",
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

func TestBootstrapKubeconfig_validateTokenExpiresAt(t *testing.T) {
	now := time.Now()

	testCases := []struct {
		name      string
		expiresAt *metav1.Time
		wantErr   bool
	}{
		{
			name:      "no expiry is accepted, the defaulter fills it in",
			expiresAt: nil,
		},
		{
			name:      "an expiry inside the window is accepted",
			expiresAt: ptrTime(now.Add(DefaultTokenExpiry)),
		},
		{
			name:      "an expiry beyond the window is rejected",
			expiresAt: ptrTime(now.Add(MaxTokenExpiryWindow + time.Minute)),
			wantErr:   true,
		},
		{
			name:      "an expiry far in the future is rejected, so a caller cannot ask for a token that never dies",
			expiresAt: ptrTime(now.AddDate(1, 0, 0)),
			wantErr:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &BootstrapKubeconfig{
				Spec: BootstrapKubeconfigSpec{TokenExpiresAt: tc.expiresAt},
			}

			err := obj.validateTokenExpiresAt(now)

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			assert.NoError(t, err)
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
			mutate:  func(o *BootstrapKubeconfig) { o.Spec.HostName = "coke-worker-2" },
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

func ptrTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}
