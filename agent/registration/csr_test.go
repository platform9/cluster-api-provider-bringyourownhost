// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: nolintlint,testpackage
package registration

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	restclient "k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/keyutil"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	testTenant     = "example-tenant"
	testHostName   = "example-worker-1"
	testTokenID    = "abcdef"
	testToken      = testTokenID + ".0123456789abcdef"
	createdCSRUID  = "created-uid"
	existingCSRUID = "existing-uid"
	oldIdentity    = "byoh:host:" + testTenant + ":" + testHostName + "-old00"
	oldCSRName     = "byoh-csr-" + testTenant + "-" + testHostName + "-old00"
)

// newTestByohCSR returns a ByohCSR that keeps its state in dir and talks to a
// fake clientset holding objects. Every CSR the fake creates gets createdCSRUID.
func newTestByohCSR(t *testing.T, dir string, objects ...runtime.Object) (*ByohCSR, *fake.Clientset) {
	t.Helper()

	client := fake.NewClientset(objects...)
	client.PrependReactor("create", "certificatesigningrequests", func(action clienttesting.Action) (bool, runtime.Object, error) {
		created, ok := action.(clienttesting.CreateAction).GetObject().(*certv1.CertificateSigningRequest)
		require.True(t, ok)
		created.UID = createdCSRUID
		// Not handled, so the fake tracker stores the object with its UID.
		return false, created, nil
	})

	return &ByohCSR{
		bootstrapClientConfig: &restclient.Config{BearerToken: testToken},
		bootstrapClient:       client,
		configPath:            filepath.Join(dir, "config"),
		logger:                logr.Discard(),
		expiryDuration:        time.Hour,
	}, client
}

func readState(t *testing.T, dir string) *State {
	t.Helper()

	got, err := loadState(dir)
	require.NoError(t, err)
	return got
}

func TestBootstrapTokenID(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		token   string
		want    string
		wantErr bool
	}{
		{
			name:  "valid bootstrap token returns only the id",
			token: testToken,
			want:  testTokenID,
		},
		{
			name:    "no bearer token",
			token:   "",
			wantErr: true,
		},
		{
			name:    "bearer token that is not a bootstrap token",
			token:   "not-a-bootstrap-token",
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := bootstrapTokenID(&restclient.Config{BearerToken: tc.token})

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNewCSRName(t *testing.T) {
	t.Parallel()

	longHostName := strings.Repeat("a", 63)
	wantPrefix := "byoh-csr-" + testTenant + "-" + testHostName + "-"

	first := newCSRName(testTenant, testHostName)
	second := newCSRName(testTenant, testHostName)
	assert.NotEqual(t, first, second, "every CSR gets a new random part")
	assert.True(t, strings.HasPrefix(first, wantPrefix), first)
	assert.Len(t, strings.TrimPrefix(first, wantPrefix), randomPartLength)

	longName := newCSRName(testTenant, longHostName)
	assert.True(t, strings.HasPrefix(longName, "byoh-csr-"+testTenant+"-"+longHostName+"-"), longName)
	assert.Empty(t, validation.IsDNS1123Subdomain(longName))
}

func TestNewIdentity(t *testing.T) {
	t.Parallel()

	identity := newIdentity(testTenant, testHostName)

	tenant, hostName, suffix, err := infrastructurev1beta1.ParseHostIdentity(identity)
	require.NoError(t, err)
	assert.Equal(t, testTenant, tenant)
	assert.Equal(t, testHostName, hostName)
	assert.Len(t, suffix, 5)
	assert.Regexp(t, "^[bcdfghjklmnpqrstvwxz2456789]{5}$", suffix)

	assert.NotEqual(t, identity, newIdentity(testTenant, testHostName), "every onboarding gets a new suffix")
}

func TestState_Matches(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		identity string
		wantErr  bool
	}{
		{
			name:     "same tenant and host name",
			identity: "byoh:host:" + testTenant + ":" + testHostName + "-abc12",
		},
		{
			name:     "different tenant",
			identity: "byoh:host:other-tenant:" + testHostName + "-abc12",
			wantErr:  true,
		},
		{
			name:     "different host name",
			identity: "byoh:host:" + testTenant + ":other-host-abc12",
			wantErr:  true,
		},
		{
			name:     "identity that does not parse",
			identity: "not-an-identity",
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := &State{Identity: tc.identity}

			err := st.matches(testTenant, testHostName)

			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestByohCSR_LoadOrCreateState(t *testing.T) {
	t.Parallel()

	const oldKey = "old-key"
	oldState := func(tokenID string) *State {
		return &State{
			Version:          stateVersion,
			Phase:            PhasePending,
			BootstrapTokenID: tokenID,
			Identity:         oldIdentity,
			CSRName:          oldCSRName,
			CSRUID:           existingCSRUID,
		}
	}

	testCases := []struct {
		name string
		// seed writes the files that an earlier run left behind.
		seed        func(t *testing.T, dir string)
		wantResumed bool
	}{
		{
			name:        "no state file starts fresh",
			seed:        func(_ *testing.T, _ string) {},
			wantResumed: false,
		},
		{
			name: "invalid JSON starts fresh",
			seed: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, StateFileName), []byte("{"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, PrivateKeyFileName), []byte(oldKey), 0o600))
			},
			wantResumed: false,
		},
		{
			name: "unknown version starts fresh",
			seed: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, StateFileName), []byte(`{"version":99}`), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, PrivateKeyFileName), []byte(oldKey), 0o600))
			},
			wantResumed: false,
		},
		{
			name: "unknown phase starts fresh",
			seed: func(t *testing.T, dir string) {
				t.Helper()
				contents := `{"version":1,"phase":"done","bootstrapTokenID":"` + testTokenID + `","identity":"` + oldIdentity + `"}`
				require.NoError(t, os.WriteFile(filepath.Join(dir, StateFileName), []byte(contents), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, PrivateKeyFileName), []byte(oldKey), 0o600))
			},
			wantResumed: false,
		},
		{
			name: "different bootstrap token id starts fresh",
			seed: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, oldState("uvwxyz").save(dir))
				require.NoError(t, os.WriteFile(filepath.Join(dir, PrivateKeyFileName), []byte(oldKey), 0o600))
			},
			wantResumed: false,
		},
		{
			name: "same token id but a different tenant starts fresh",
			seed: func(t *testing.T, dir string) {
				t.Helper()
				other := oldState(testTokenID)
				other.Identity = "byoh:host:other-tenant:" + testHostName + "-old00"
				require.NoError(t, other.save(dir))
				require.NoError(t, os.WriteFile(filepath.Join(dir, PrivateKeyFileName), []byte(oldKey), 0o600))
			},
			wantResumed: false,
		},
		{
			name: "same token id but a different host name starts fresh",
			seed: func(t *testing.T, dir string) {
				t.Helper()
				other := oldState(testTokenID)
				other.Identity = "byoh:host:" + testTenant + ":other-host-old00"
				require.NoError(t, other.save(dir))
				require.NoError(t, os.WriteFile(filepath.Join(dir, PrivateKeyFileName), []byte(oldKey), 0o600))
			},
			wantResumed: false,
		},
		{
			name: "same token id but missing key starts fresh",
			seed: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, oldState(testTokenID).save(dir))
			},
			wantResumed: false,
		},
		{
			name: "same token id resumes and deletes nothing",
			seed: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, oldState(testTokenID).save(dir))
				require.NoError(t, os.WriteFile(filepath.Join(dir, PrivateKeyFileName), []byte(oldKey), 0o600))
			},
			wantResumed: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			tc.seed(t, dir)
			bcsr, _ := newTestByohCSR(t, dir)

			st, keyData, resumed, err := bcsr.loadOrCreateState(dir, testTenant, testHostName, testTokenID, "")
			require.NoError(t, err)
			assert.Equal(t, tc.wantResumed, resumed)
			assert.Equal(t, testTokenID, st.BootstrapTokenID)

			saved := readState(t, dir)
			assert.Equal(t, st, saved)

			keyOnDisk, err := os.ReadFile(filepath.Join(dir, PrivateKeyFileName))
			require.NoError(t, err)
			assert.Equal(t, keyOnDisk, keyData)

			keyInfo, err := os.Stat(filepath.Join(dir, PrivateKeyFileName))
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())
			stateInfo, err := os.Stat(filepath.Join(dir, StateFileName))
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), stateInfo.Mode().Perm())

			if tc.wantResumed {
				assert.Equal(t, oldState(testTokenID), st)
				assert.Equal(t, []byte(oldKey), keyData)
				return
			}

			// A fresh start gets a new identity, a new CSR name, no CSR UID and a
			// key that can be parsed.
			assert.Equal(t, PhasePending, st.Phase)
			assert.NotEqual(t, oldState(testTokenID).Identity, st.Identity)
			assert.NotEqual(t, oldState(testTokenID).CSRName, st.CSRName)
			assert.Empty(t, st.CSRUID)
			_, err = keyutil.ParsePrivateKeyPEM(keyData)
			assert.NoError(t, err)

			tenant, hostName, _, err := infrastructurev1beta1.ParseHostIdentity(st.Identity)
			require.NoError(t, err)
			assert.Equal(t, testTenant, tenant)
			assert.Equal(t, testHostName, hostName)
		})
	}
}

func TestByohCSR_EnsureCSR_Fresh(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bcsr, client := newTestByohCSR(t, dir)

	st, keyData, resumed, err := bcsr.loadOrCreateState(dir, testTenant, testHostName, testTokenID, "")
	require.NoError(t, err)
	require.False(t, resumed)
	privateKey, err := keyutil.ParsePrivateKeyPEM(keyData)
	require.NoError(t, err)

	name, uid, err := bcsr.ensureCSR(t.Context(), dir, st, testTenant, testHostName, privateKey, resumed, false)
	require.NoError(t, err)

	assert.Equal(t, st.CSRName, name)
	assert.Equal(t, createdCSRUID, string(uid))
	saved := readState(t, dir)
	assert.Equal(t, name, saved.CSRName)
	assert.Equal(t, createdCSRUID, saved.CSRUID)

	created, err := client.CertificatesV1().CertificateSigningRequests().Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	block, _ := pem.Decode(created.Spec.Request)
	require.NotNil(t, block)
	request, err := x509.ParseCertificateRequest(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, st.Identity, request.Subject.CommonName)
	assert.Equal(t, []string{ByohCSROrg}, request.Subject.Organization)
}

func TestByohCSR_EnsureCSR_Resume(t *testing.T) {
	t.Parallel()

	approved := certv1.CertificateSigningRequestCondition{Type: certv1.CertificateApproved, Status: corev1.ConditionTrue}
	denied := certv1.CertificateSigningRequestCondition{Type: certv1.CertificateDenied, Status: corev1.ConditionTrue}
	failed := certv1.CertificateSigningRequestCondition{Type: certv1.CertificateFailed, Status: corev1.ConditionTrue}

	testCases := []struct {
		name string
		// existing is the CSR the state names; nil means it does not exist.
		existing *certv1.CertificateSigningRequest
		// stateUID is the CSR UID recorded in the state.
		stateUID  string
		wantReuse bool
	}{
		{
			name: "issued CSR is reused",
			existing: &certv1.CertificateSigningRequest{
				Status: certv1.CertificateSigningRequestStatus{
					Conditions:  []certv1.CertificateSigningRequestCondition{approved},
					Certificate: []byte("certificate"),
				},
			},
			stateUID:  existingCSRUID,
			wantReuse: true,
		},
		{
			name:      "pending CSR is reused",
			existing:  &certv1.CertificateSigningRequest{},
			stateUID:  existingCSRUID,
			wantReuse: true,
		},
		{
			name:      "CSR created but UID not yet recorded is reused",
			existing:  &certv1.CertificateSigningRequest{},
			stateUID:  "",
			wantReuse: true,
		},
		{
			name:      "CSR not found gets a new CSR",
			existing:  nil,
			stateUID:  existingCSRUID,
			wantReuse: false,
		},
		{
			name: "denied CSR gets a new CSR",
			existing: &certv1.CertificateSigningRequest{
				Status: certv1.CertificateSigningRequestStatus{
					Conditions: []certv1.CertificateSigningRequestCondition{denied},
				},
			},
			stateUID:  existingCSRUID,
			wantReuse: false,
		},
		{
			name: "failed CSR gets a new CSR",
			existing: &certv1.CertificateSigningRequest{
				Status: certv1.CertificateSigningRequestStatus{
					Conditions: []certv1.CertificateSigningRequestCondition{failed},
				},
			},
			stateUID:  existingCSRUID,
			wantReuse: false,
		},
		{
			name:      "CSR with a different UID gets a new CSR",
			existing:  &certv1.CertificateSigningRequest{},
			stateUID:  "some-other-uid",
			wantReuse: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			var objects []runtime.Object
			if tc.existing != nil {
				tc.existing.Name = oldCSRName
				tc.existing.UID = existingCSRUID
				objects = append(objects, tc.existing)
			}
			bcsr, client := newTestByohCSR(t, dir, objects...)

			seeded := &State{
				Version:          stateVersion,
				Phase:            PhasePending,
				BootstrapTokenID: testTokenID,
				Identity:         oldIdentity,
				CSRName:          oldCSRName,
				CSRUID:           tc.stateUID,
			}
			require.NoError(t, seeded.save(dir))
			keyData, _, err := keyutil.LoadOrGenerateKeyFile(filepath.Join(dir, PrivateKeyFileName))
			require.NoError(t, err)
			privateKey, err := keyutil.ParsePrivateKeyPEM(keyData)
			require.NoError(t, err)

			st, keyOnResume, resumed, err := bcsr.loadOrCreateState(dir, testTenant, testHostName, testTokenID, "")
			require.NoError(t, err)
			require.True(t, resumed)
			assert.Equal(t, keyData, keyOnResume)

			name, uid, err := bcsr.ensureCSR(t.Context(), dir, st, testTenant, testHostName, privateKey, resumed, false)
			require.NoError(t, err)

			list, err := client.CertificatesV1().CertificateSigningRequests().List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)
			saved := readState(t, dir)

			// The identity and the key are kept either way.
			assert.Equal(t, seeded.Identity, saved.Identity)
			assert.Equal(t, name, saved.CSRName)
			assert.Equal(t, string(uid), saved.CSRUID)

			if tc.wantReuse {
				assert.Equal(t, oldCSRName, name)
				assert.Equal(t, existingCSRUID, string(uid))
				assert.Len(t, list.Items, 1)
				return
			}

			assert.NotEqual(t, oldCSRName, name)
			assert.True(t, strings.HasPrefix(name, "byoh-csr-"+testTenant+"-"+testHostName+"-"), name)
			assert.Equal(t, createdCSRUID, string(uid))
			created, err := client.CertificatesV1().CertificateSigningRequests().Get(t.Context(), name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, createdCSRUID, string(created.UID))
		})
	}
}

func TestByohCSR_NewOnboardingGetsNewIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, _ := newTestByohCSR(t, dir)
	firstState, _, _, err := first.loadOrCreateState(dir, testTenant, testHostName, testTokenID, "")
	require.NoError(t, err)

	// The same host onboards again with a new bootstrap token.
	secondState, _, resumed, err := first.loadOrCreateState(dir, testTenant, testHostName, "uvwxyz", "")
	require.NoError(t, err)

	assert.False(t, resumed)
	assert.NotEqual(t, firstState.Identity, secondState.Identity)
	assert.NotEqual(t, firstState.CSRName, secondState.CSRName)
	assert.Equal(t, "uvwxyz", readState(t, dir).BootstrapTokenID)
}

func TestByohCSR_LoadOrCreateState_ExistingIdentity(t *testing.T) {
	t.Parallel()

	const existing = "byoh:host:" + testTenant + ":" + testHostName + "-kept2"

	t.Run("fresh state uses the existing identity", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		bcsr, _ := newTestByohCSR(t, dir)

		st, _, resumed, err := bcsr.loadOrCreateState(dir, testTenant, testHostName, testTokenID, existing)
		require.NoError(t, err)

		assert.False(t, resumed)
		assert.Equal(t, existing, st.Identity)
		assert.Equal(t, existing, readState(t, dir).Identity)
	})

	t.Run("resumed state keeps its own identity", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		bcsr, _ := newTestByohCSR(t, dir)
		seeded := &State{Version: stateVersion, Phase: PhasePending, BootstrapTokenID: testTokenID, Identity: oldIdentity, CSRName: oldCSRName}
		require.NoError(t, seeded.save(dir))
		require.NoError(t, os.WriteFile(filepath.Join(dir, PrivateKeyFileName), []byte("old-key"), 0o600))

		st, _, resumed, err := bcsr.loadOrCreateState(dir, testTenant, testHostName, testTokenID, existing)
		require.NoError(t, err)

		assert.True(t, resumed)
		assert.Equal(t, oldIdentity, st.Identity)
	})
}

func TestByohCSR_ReusableCSR_NoCSRName(t *testing.T) {
	t.Parallel()

	bcsr, client := newTestByohCSR(t, t.TempDir())

	uid, reusable, err := bcsr.reusableCSR(t.Context(), &State{Identity: oldIdentity})
	require.NoError(t, err)

	assert.False(t, reusable)
	assert.Empty(t, uid)
	assert.Empty(t, client.Actions(), "a state without a CSR name makes no API call")
}

func TestByohCSR_EnsureCSR_RenewsAfterIssued(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// The old CSR still exists and looks reusable, so only the issued phase
	// makes the agent skip it.
	oldCSR := &certv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: oldCSRName, UID: existingCSRUID},
	}
	bcsr, client := newTestByohCSR(t, dir, oldCSR)

	seeded := &State{
		Version:          stateVersion,
		Phase:            PhaseIssued,
		BootstrapTokenID: testTokenID,
		Identity:         oldIdentity,
		CSRName:          oldCSRName,
		CSRUID:           existingCSRUID,
	}
	require.NoError(t, seeded.save(dir))
	keyData, _, err := keyutil.LoadOrGenerateKeyFile(filepath.Join(dir, PrivateKeyFileName))
	require.NoError(t, err)
	privateKey, err := keyutil.ParsePrivateKeyPEM(keyData)
	require.NoError(t, err)

	st, _, resumed, err := bcsr.loadOrCreateState(dir, testTenant, testHostName, testTokenID, "")
	require.NoError(t, err)
	require.True(t, resumed)
	require.Equal(t, PhaseIssued, st.Phase)

	name, uid, err := bcsr.ensureCSR(t.Context(), dir, st, testTenant, testHostName, privateKey, resumed, false)
	require.NoError(t, err)

	assert.NotEqual(t, oldCSRName, name)
	assert.True(t, strings.HasPrefix(name, "byoh-csr-"+testTenant+"-"+testHostName+"-"), name)
	assert.Equal(t, createdCSRUID, string(uid))

	saved := readState(t, dir)
	assert.Equal(t, PhasePending, saved.Phase)
	assert.Equal(t, name, saved.CSRName)
	assert.Equal(t, createdCSRUID, saved.CSRUID)
	assert.Equal(t, oldIdentity, saved.Identity)

	list, err := client.CertificatesV1().CertificateSigningRequests().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list.Items, 2, "the old CSR is left alone and a new one is created")
}

func TestByohCSR_EnsureCSR_Renewal(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		phase     Phase
		renewal   bool
		wantReuse bool
	}{
		{
			name:      "renewal with a pending state gets a new CSR",
			phase:     PhasePending,
			renewal:   true,
			wantReuse: false,
		},
		{
			name:      "first onboarding with a pending state reuses the CSR",
			phase:     PhasePending,
			renewal:   false,
			wantReuse: true,
		},
		{
			name:      "renewal with an issued state gets a new CSR",
			phase:     PhaseIssued,
			renewal:   true,
			wantReuse: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			// The old CSR exists and looks reusable, so only the renewal or the
			// issued phase makes the agent skip it.
			oldCSR := &certv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{Name: oldCSRName, UID: existingCSRUID},
			}
			bcsr, client := newTestByohCSR(t, dir, oldCSR)

			seeded := &State{
				Version:          stateVersion,
				Phase:            tc.phase,
				BootstrapTokenID: testTokenID,
				Identity:         oldIdentity,
				CSRName:          oldCSRName,
				CSRUID:           existingCSRUID,
			}
			require.NoError(t, seeded.save(dir))
			keyData, _, err := keyutil.LoadOrGenerateKeyFile(filepath.Join(dir, PrivateKeyFileName))
			require.NoError(t, err)
			privateKey, err := keyutil.ParsePrivateKeyPEM(keyData)
			require.NoError(t, err)

			st, _, resumed, err := bcsr.loadOrCreateState(dir, testTenant, testHostName, testTokenID, "")
			require.NoError(t, err)
			require.True(t, resumed)

			name, uid, err := bcsr.ensureCSR(t.Context(), dir, st, testTenant, testHostName, privateKey, resumed, tc.renewal)
			require.NoError(t, err)

			saved := readState(t, dir)
			list, err := client.CertificatesV1().CertificateSigningRequests().List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)
			assert.Equal(t, oldIdentity, saved.Identity)
			assert.Equal(t, name, saved.CSRName)
			assert.Equal(t, string(uid), saved.CSRUID)
			assert.Equal(t, PhasePending, saved.Phase)

			if tc.wantReuse {
				assert.Equal(t, oldCSRName, name)
				assert.Equal(t, existingCSRUID, string(uid))
				assert.Len(t, list.Items, 1)
				return
			}

			assert.NotEqual(t, oldCSRName, name)
			assert.True(t, strings.HasPrefix(name, "byoh-csr-"+testTenant+"-"+testHostName+"-"), name)
			assert.Equal(t, createdCSRUID, string(uid))
			assert.Len(t, list.Items, 2, "the old CSR is left alone and a new one is created")
		})
	}
}
