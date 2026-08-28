// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const enrollmentHostName = "host1"

func enrollmentTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	err := AddToScheme(scheme)
	require.NoError(t, err)

	return scheme
}

// newEnrollmentValidator builds a validator whose client holds the given
// objects.
func newEnrollmentValidator(t *testing.T, objects ...*ByoHost) *ByoHostEnrollmentValidator {
	t.Helper()

	builder := fake.NewClientBuilder().WithScheme(enrollmentTestScheme(t))
	for _, object := range objects {
		builder = builder.WithObjects(object)
	}

	return &ByoHostEnrollmentValidator{Client: builder.Build()}
}

func newByoHost(name, namespace string) *ByoHost {
	return &ByoHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func newEnrollment(name string, spec ByoHostEnrollmentSpec) *ByoHostEnrollment {
	return &ByoHostEnrollment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: DefaultNamespace,
		},
		Spec: spec,
	}
}

func TestValidateEnrollmentName(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		hostName    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "already normalized name is accepted",
			hostName: "host-01.example.com",
		},
		{
			name:        "uppercase name is rejected",
			hostName:    "Host-01",
			wantErr:     true,
			errContains: `use "host-01"`,
		},
		{
			name:        "underscore is rejected",
			hostName:    "host_01",
			wantErr:     true,
			errContains: `use "host-01"`,
		},
		{
			name:        "trailing dot is rejected",
			hostName:    "host-01.",
			wantErr:     true,
			errContains: `use "host-01"`,
		},
		{
			name:        "name that cannot be normalized is rejected",
			hostName:    "host!01",
			wantErr:     true,
			errContains: "not a valid object name",
		},
		{
			name:        "empty name is rejected",
			hostName:    "",
			wantErr:     true,
			errContains: "not a valid object name",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateEnrollmentName(tc.hostName)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errContains)
		})
	}
}

func TestEffectiveTokenTTL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		ttl  *metav1.Duration
		want time.Duration
	}{
		{
			name: "unset falls back to the CRD default",
			ttl:  nil,
			want: defaultTokenTTL,
		},
		{
			name: "set value is used as is",
			ttl:  &metav1.Duration{Duration: 2 * time.Hour},
			want: 2 * time.Hour,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := effectiveTokenTTL(tc.ttl)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidateTokenTTL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		ttl     *metav1.Duration
		wantErr bool
	}{
		{
			name: "unset is accepted",
			ttl:  nil,
		},
		{
			name: "below the ceiling is accepted",
			ttl:  &metav1.Duration{Duration: 30 * time.Minute},
		},
		{
			name: "exactly the ceiling is accepted",
			ttl:  &metav1.Duration{Duration: MaxTokenTTL},
		},
		{
			name:    "above the ceiling is rejected",
			ttl:     &metav1.Duration{Duration: MaxTokenTTL + time.Minute},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateTokenTTL(tc.ttl)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), "must not exceed 24h0m0s")
		})
	}
}

func TestValidateValidUntil(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 28, 10, 0, 0, 0, time.UTC)
	ttl := time.Hour

	testCases := []struct {
		name        string
		validUntil  *metav1.Time
		wantErr     bool
		errContains string
	}{
		{
			name:       "unset is accepted",
			validUntil: nil,
		},
		{
			name:       "exactly one TTL away is accepted",
			validUntil: &metav1.Time{Time: now.Add(ttl)},
		},
		{
			name:       "well beyond one TTL is accepted",
			validUntil: &metav1.Time{Time: now.Add(72 * time.Hour)},
		},
		{
			name:        "in the past is rejected",
			validUntil:  &metav1.Time{Time: now.Add(-time.Minute)},
			wantErr:     true,
			errContains: "must be in the future",
		},
		{
			name:        "exactly now is rejected",
			validUntil:  &metav1.Time{Time: now},
			wantErr:     true,
			errContains: "must be in the future",
		},
		{
			name:        "sooner than one TTL is rejected",
			validUntil:  &metav1.Time{Time: now.Add(ttl - time.Minute)},
			wantErr:     true,
			errContains: "at least one tokenTTL (1h0m0s) from now",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateValidUntil(tc.validUntil, ttl, now)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errContains)
		})
	}
}

func TestValidateHostNotRegistered(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		existingHosts []*ByoHost
		wantErr       bool
	}{
		{
			name: "no ByoHost of that name is accepted",
		},
		{
			name:          "a ByoHost of the same name in another namespace is accepted",
			existingHosts: []*ByoHost{newByoHost(enrollmentHostName, "other-namespace")},
		},
		{
			name:          "a ByoHost of the same name and namespace is rejected",
			existingHosts: []*ByoHost{newByoHost(enrollmentHostName, DefaultNamespace)},
			wantErr:       true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			validator := newEnrollmentValidator(t, tc.existingHosts...)
			enrollment := newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{})

			err := validator.validateHostNotRegistered(t.Context(), enrollment)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), "off-board that host before enrolling it again")
		})
	}
}

// TestValidateHostNotRegisteredLookupFails covers the check failing closed. A
// lookup that cannot say whether the host is registered must reject the
// enrollment rather than read the failure as "no such host".
func TestValidateHostNotRegisteredLookupFails(t *testing.T) {
	t.Parallel()

	lookupErr := errors.New("apiserver unreachable")
	fakeClient := fake.NewClientBuilder().
		WithScheme(enrollmentTestScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
				return lookupErr
			},
		}).
		Build()
	validator := &ByoHostEnrollmentValidator{Client: fakeClient}
	enrollment := newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{})

	err := validator.validateHostNotRegistered(t.Context(), enrollment)
	require.Error(t, err)
	assert.ErrorIs(t, err, lookupErr)
}

func TestByoHostEnrollmentValidatorValidateCreate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		enrollment    *ByoHostEnrollment
		existingHosts []*ByoHost
		wantErr       bool
		errContains   string
	}{
		{
			name: "every rule satisfied is admitted",
			enrollment: newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{
				TokenTTL:   &metav1.Duration{Duration: time.Hour},
				ValidUntil: &metav1.Time{Time: time.Now().Add(48 * time.Hour)},
			}),
		},
		{
			name:       "an empty spec is admitted",
			enrollment: newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{}),
		},
		{
			name:        "a name that is not normalized is rejected",
			enrollment:  newEnrollment("Host1", ByoHostEnrollmentSpec{}),
			wantErr:     true,
			errContains: "host name is not normalized",
		},
		{
			name: "a tokenTTL above the ceiling is rejected",
			enrollment: newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{
				TokenTTL: &metav1.Duration{Duration: 25 * time.Hour},
			}),
			wantErr:     true,
			errContains: "must not exceed 24h0m0s",
		},
		{
			name: "a validUntil in the past is rejected",
			enrollment: newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{
				ValidUntil: &metav1.Time{Time: time.Now().Add(-time.Hour)},
			}),
			wantErr:     true,
			errContains: "must be in the future",
		},
		{
			name: "a validUntil sooner than one tokenTTL is rejected",
			enrollment: newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{
				TokenTTL:   &metav1.Duration{Duration: time.Hour},
				ValidUntil: &metav1.Time{Time: time.Now().Add(30 * time.Minute)},
			}),
			wantErr:     true,
			errContains: "at least one tokenTTL",
		},
		{
			name:          "an already registered host is rejected",
			enrollment:    newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{}),
			existingHosts: []*ByoHost{newByoHost(enrollmentHostName, DefaultNamespace)},
			wantErr:       true,
			errContains:   "off-board that host before enrolling it again",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			validator := newEnrollmentValidator(t, tc.existingHosts...)

			warnings, err := validator.ValidateCreate(t.Context(), tc.enrollment)
			assert.Nil(t, warnings)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errContains)
		})
	}
}

func TestByoHostEnrollmentValidatorValidateCreateWrongType(t *testing.T) {
	t.Parallel()

	validator := newEnrollmentValidator(t)

	_, err := validator.ValidateCreate(t.Context(), &ByoHost{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a ByoHostEnrollment")
}

func TestByoHostEnrollmentValidatorValidateUpdate(t *testing.T) {
	t.Parallel()

	spec := ByoHostEnrollmentSpec{
		TokenTTL:   &metav1.Duration{Duration: time.Hour},
		ValidUntil: &metav1.Time{Time: time.Now().Add(48 * time.Hour).Truncate(time.Second)},
	}

	changedTTL := *spec.DeepCopy()
	changedTTL.TokenTTL = &metav1.Duration{Duration: 2 * time.Hour}

	clearedValidUntil := *spec.DeepCopy()
	clearedValidUntil.ValidUntil = nil

	testCases := []struct {
		name    string
		mutate  func(enrollment *ByoHostEnrollment)
		wantErr bool
	}{
		{
			name:   "an unchanged object is admitted",
			mutate: func(_ *ByoHostEnrollment) {},
		},
		{
			name: "a status-only change is admitted",
			mutate: func(enrollment *ByoHostEnrollment) {
				enrollment.Status.TokenID = "abcdef"
				enrollment.Status.ObservedGeneration = 2
			},
		},
		{
			name: "a label change is admitted",
			mutate: func(enrollment *ByoHostEnrollment) {
				enrollment.Labels = map[string]string{"pcd-kaapi.pf9.io/region": "r1"}
			},
		},
		{
			name: "a changed tokenTTL is rejected",
			mutate: func(enrollment *ByoHostEnrollment) {
				enrollment.Spec = changedTTL
			},
			wantErr: true,
		},
		{
			name: "a cleared validUntil is rejected",
			mutate: func(enrollment *ByoHostEnrollment) {
				enrollment.Spec = clearedValidUntil
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			validator := newEnrollmentValidator(t)
			oldEnrollment := newEnrollment(enrollmentHostName, *spec.DeepCopy())
			updated := oldEnrollment.DeepCopy()
			tc.mutate(updated)

			warnings, err := validator.ValidateUpdate(t.Context(), oldEnrollment, updated)
			assert.Nil(t, warnings)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), "spec is immutable")
		})
	}
}

func TestByoHostEnrollmentValidatorValidateUpdateWrongType(t *testing.T) {
	t.Parallel()

	validator := newEnrollmentValidator(t)
	enrollment := newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{})

	_, err := validator.ValidateUpdate(t.Context(), &ByoHost{}, enrollment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a ByoHostEnrollment")

	_, err = validator.ValidateUpdate(t.Context(), enrollment, &ByoHost{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a ByoHostEnrollment")
}

func TestByoHostEnrollmentValidatorValidateDelete(t *testing.T) {
	t.Parallel()

	validator := newEnrollmentValidator(t)
	enrollment := newEnrollment(enrollmentHostName, ByoHostEnrollmentSpec{})

	warnings, err := validator.ValidateDelete(t.Context(), enrollment)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}
