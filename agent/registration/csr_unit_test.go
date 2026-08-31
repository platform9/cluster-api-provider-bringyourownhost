// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: nolintlint,testpackage
package registration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	certv1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/util/keyutil"
)

const testHostName = "test-host"

// localTestKey moves the test into a scratch directory and puts a private
// key at TmpPrivateKey there, which is where the agent looks for the key it
// created on an earlier attempt. TmpPrivateKey is a relative path, so the
// working directory is the only way to redirect it.
func localTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	t.Chdir(t.TempDir())

	key, err := rsa.GenerateKey(rand.Reader, KeySize)
	require.NoError(t, err)

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  keyutil.RSAPrivateKeyBlockType,
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	err = keyutil.WriteKey(TmpPrivateKey, keyPEM)
	require.NoError(t, err)

	return key
}

// newTestCSR builds a certificate signing request object for hostname signed
// with key, carrying one condition per entry in conditions.
func newTestCSR(t *testing.T, name, hostname string, key *rsa.PrivateKey, conditions ...certv1.RequestConditionType) *certv1.CertificateSigningRequest {
	t.Helper()

	csrData, err := generateCSR(hostname, key)
	require.NoError(t, err)

	req := &certv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				infrastructurev1beta1.HostCSRLabel: hostCSRLabelValue(hostname),
			},
		},
		Spec: certv1.CertificateSigningRequestSpec{
			Request:    csrData,
			SignerName: certv1.KubeAPIServerClientSignerName,
			Usages:     []certv1.KeyUsage{certv1.UsageClientAuth},
		},
	}

	for _, condition := range conditions {
		req.Status.Conditions = append(req.Status.Conditions, certv1.CertificateSigningRequestCondition{
			Type:   condition,
			Status: "True",
		})
	}

	return req
}

func TestHostCSRLabelValue(t *testing.T) {
	longName := strings.Repeat("a", infrastructurev1beta1.MaxK8sLabelValueLength) + "-suffix"
	otherLongName := strings.Repeat("a", infrastructurev1beta1.MaxK8sLabelValueLength) + "-other"

	testCases := []struct {
		name     string
		hostname string
		want     string
	}{
		{
			name:     "short host name is used as is",
			hostname: testHostName,
			want:     testHostName,
		},
		{
			name:     "host name exactly at the limit is used as is",
			hostname: strings.Repeat("b", infrastructurev1beta1.MaxK8sLabelValueLength),
			want:     strings.Repeat("b", infrastructurev1beta1.MaxK8sLabelValueLength),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := hostCSRLabelValue(tc.hostname)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), infrastructurev1beta1.MaxK8sLabelValueLength)
		})
	}

	t.Run("over-long host name keeps a prefix and a hash", func(t *testing.T) {
		got := hostCSRLabelValue(longName)

		assert.Len(t, got, infrastructurev1beta1.MaxK8sLabelValueLength)
		assert.True(t, strings.HasPrefix(got, strings.Repeat("a", infrastructurev1beta1.MaxLabelPrefixLength)+infrastructurev1beta1.LabelSeparator))
	})

	t.Run("two over-long host names sharing a prefix stay distinct", func(t *testing.T) {
		assert.NotEqual(t, hostCSRLabelValue(longName), hostCSRLabelValue(otherLongName))
	})

	t.Run("the same host name always maps to the same value", func(t *testing.T) {
		assert.Equal(t, hostCSRLabelValue(longName), hostCSRLabelValue(longName))
	})
}

func TestSelectCSR(t *testing.T) {
	key := localTestKey(t)

	pending := newTestCSR(t, "byoh-csr-pending", testHostName, key)
	approved := newTestCSR(t, "byoh-csr-approved", testHostName, key, certv1.CertificateApproved)
	denied := newTestCSR(t, "byoh-csr-denied", testHostName, key, certv1.CertificateDenied)
	failed := newTestCSR(t, "byoh-csr-failed", testHostName, key, certv1.CertificateFailed)

	testCases := []struct {
		name      string
		items     []certv1.CertificateSigningRequest
		wantState csrState
		wantName  string
	}{
		{
			name:      "nothing exists",
			items:     nil,
			wantState: csrStateNone,
		},
		{
			name:      "a pending request is waited on",
			items:     []certv1.CertificateSigningRequest{*pending},
			wantState: csrStatePending,
			wantName:  "byoh-csr-pending",
		},
		{
			name:      "an approved request is picked up",
			items:     []certv1.CertificateSigningRequest{*approved},
			wantState: csrStateApproved,
			wantName:  "byoh-csr-approved",
		},
		{
			name:      "a denied request leaves nothing usable",
			items:     []certv1.CertificateSigningRequest{*denied},
			wantState: csrStateRejected,
		},
		{
			name:      "a failed request leaves nothing usable",
			items:     []certv1.CertificateSigningRequest{*failed},
			wantState: csrStateRejected,
		},
		{
			name:      "an approved request wins over a pending one",
			items:     []certv1.CertificateSigningRequest{*pending, *approved},
			wantState: csrStateApproved,
			wantName:  "byoh-csr-approved",
		},
		{
			name:      "a pending request wins over a denied one",
			items:     []certv1.CertificateSigningRequest{*denied, *pending},
			wantState: csrStatePending,
			wantName:  "byoh-csr-pending",
		},
		{
			name:      "an approved request wins over a pending and denied one",
			items:     []certv1.CertificateSigningRequest{*denied, *pending, *approved},
			wantState: csrStateApproved,
			wantName:  "byoh-csr-approved",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, state := selectCSR(tc.items)

			assert.Equal(t, tc.wantState, state)
			if tc.wantName == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.wantName, got.Name)
		})
	}
}

func TestRequestMatchesKey(t *testing.T) {
	key := localTestKey(t)

	otherKey, err := rsa.GenerateKey(rand.Reader, KeySize)
	require.NoError(t, err)

	mine := newTestCSR(t, "byoh-csr-mine", testHostName, key)
	theirs := newTestCSR(t, "byoh-csr-theirs", testHostName, otherKey)

	unparsable := newTestCSR(t, "byoh-csr-unparsable", testHostName, key)
	unparsable.Spec.Request = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: []byte("not a certificate request"),
	})

	noPEM := newTestCSR(t, "byoh-csr-no-pem", testHostName, key)
	noPEM.Spec.Request = []byte("not pem at all")

	testCases := []struct {
		name    string
		request *certv1.CertificateSigningRequest
		want    bool
		wantErr bool
	}{
		{
			name:    "request made with the local key",
			request: mine,
			want:    true,
		},
		{
			name:    "request made with a different key",
			request: theirs,
			want:    false,
		},
		{
			name:    "request body is not a certificate request",
			request: unparsable,
			wantErr: true,
		},
		{
			name:    "request body is not PEM",
			request: noPEM,
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requestMatchesKey(tc.request, key)

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCanReuse(t *testing.T) {
	testCases := []struct {
		name string
		// req builds the request canReuse is asked about. It takes the
		// private key sitting on this host's disk.
		req func(t *testing.T, key *rsa.PrivateKey) *certv1.CertificateSigningRequest
		// corruptLocalKey, when set, overwrites the local key file with
		// content that cannot be loaded as a private key.
		corruptLocalKey bool
		want            bool
	}{
		{
			name: "the request was made with the local key",
			req: func(t *testing.T, key *rsa.PrivateKey) *certv1.CertificateSigningRequest {
				return newTestCSR(t, "byoh-csr-mine", testHostName, key)
			},
			want: true,
		},
		{
			name: "the request was made with a different key",
			req: func(t *testing.T, _ *rsa.PrivateKey) *certv1.CertificateSigningRequest {
				otherKey, err := rsa.GenerateKey(rand.Reader, KeySize)
				require.NoError(t, err)
				return newTestCSR(t, "byoh-csr-stale", testHostName, otherKey)
			},
			want: false,
		},
		{
			name: "the request body cannot be compared with the local key",
			req: func(t *testing.T, key *rsa.PrivateKey) *certv1.CertificateSigningRequest {
				req := newTestCSR(t, "byoh-csr-unparsable", testHostName, key)
				req.Spec.Request = []byte("not pem at all")
				return req
			},
			want: false,
		},
		{
			name: "the local private key file cannot be loaded",
			req: func(t *testing.T, key *rsa.PrivateKey) *certv1.CertificateSigningRequest {
				return newTestCSR(t, "byoh-csr-mine", testHostName, key)
			},
			corruptLocalKey: true,
			want:            false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := localTestKey(t)
			req := tc.req(t, key)

			if tc.corruptLocalKey {
				err := keyutil.WriteKey(TmpPrivateKey, []byte("not a private key"))
				require.NoError(t, err)
			}

			bcsr := &ByohCSR{logger: logr.Discard()}

			got := bcsr.canReuse(req)

			assert.Equal(t, tc.want, got)
			if tc.want {
				keyData, err := os.ReadFile(TmpPrivateKey)
				require.NoError(t, err)
				assert.Equal(t, keyData, bcsr.PrivateKey)
			} else {
				assert.Nil(t, bcsr.PrivateKey)
			}
		})
	}
}

func TestEnsureClientCertRequest(t *testing.T) {
	otherKey, err := rsa.GenerateKey(rand.Reader, KeySize)
	require.NoError(t, err)

	testCases := []struct {
		name string
		// existing builds the requests already on the API server. It takes
		// the private key sitting on this host's disk.
		existing func(t *testing.T, key *rsa.PrivateKey) []*certv1.CertificateSigningRequest
		// reusedName is the request the agent is expected to attach to. An
		// empty value means it must create a new one instead.
		reusedName string
		wantTotal  int
	}{
		{
			name:      "nothing exists so a request is created",
			existing:  func(*testing.T, *rsa.PrivateKey) []*certv1.CertificateSigningRequest { return nil },
			wantTotal: 1,
		},
		{
			name: "a pending request is reused",
			existing: func(t *testing.T, key *rsa.PrivateKey) []*certv1.CertificateSigningRequest {
				return []*certv1.CertificateSigningRequest{newTestCSR(t, "byoh-csr-pending", testHostName, key)}
			},
			reusedName: "byoh-csr-pending",
			wantTotal:  1,
		},
		{
			name: "an approved request is reused",
			existing: func(t *testing.T, key *rsa.PrivateKey) []*certv1.CertificateSigningRequest {
				return []*certv1.CertificateSigningRequest{
					newTestCSR(t, "byoh-csr-approved", testHostName, key, certv1.CertificateApproved),
				}
			},
			reusedName: "byoh-csr-approved",
			wantTotal:  1,
		},
		{
			name: "a denied request is replaced instead of reused",
			existing: func(t *testing.T, key *rsa.PrivateKey) []*certv1.CertificateSigningRequest {
				return []*certv1.CertificateSigningRequest{
					newTestCSR(t, "byoh-csr-denied", testHostName, key, certv1.CertificateDenied),
				}
			},
			wantTotal: 2,
		},
		{
			name: "a failed request is replaced instead of reused",
			existing: func(t *testing.T, key *rsa.PrivateKey) []*certv1.CertificateSigningRequest {
				return []*certv1.CertificateSigningRequest{
					newTestCSR(t, "byoh-csr-failed", testHostName, key, certv1.CertificateFailed),
				}
			},
			wantTotal: 2,
		},
		{
			name: "a pending request made with another key is replaced",
			existing: func(t *testing.T, _ *rsa.PrivateKey) []*certv1.CertificateSigningRequest {
				return []*certv1.CertificateSigningRequest{newTestCSR(t, "byoh-csr-stale", testHostName, otherKey)}
			},
			wantTotal: 2,
		},
		{
			name: "a request belonging to another host is ignored",
			existing: func(t *testing.T, key *rsa.PrivateKey) []*certv1.CertificateSigningRequest {
				return []*certv1.CertificateSigningRequest{newTestCSR(t, "byoh-csr-other-host", "other-host", key)}
			},
			wantTotal: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := localTestKey(t)

			var objects []runtime.Object
			for _, req := range tc.existing(t, key) {
				objects = append(objects, req)
			}

			client := fake.NewClientset(objects...)
			bcsr := &ByohCSR{
				bootstrapClient: client,
				logger:          logr.Discard(),
				expiryDuration:  time.Hour,
			}

			req, err := bcsr.ensureClientCertRequest(t.Context(), testHostName)
			require.NoError(t, err)

			if tc.reusedName != "" {
				assert.Equal(t, tc.reusedName, req.Name)
			} else {
				assert.True(t, strings.HasPrefix(req.Name, ByohCSRNamePrefix+testHostName+"-"), "expected a freshly named request, got %q", req.Name)
			}

			list, err := client.CertificatesV1().CertificateSigningRequests().List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)
			assert.Len(t, list.Items, tc.wantTotal)
		})
	}

	t.Run("request rejects empty hostname", func(t *testing.T) {
		localTestKey(t)

		bcsr := &ByohCSR{
			bootstrapClient: fake.NewClientset(),
			logger:          logr.Discard(),
			expiryDuration:  time.Hour,
		}

		_, err := bcsr.ensureClientCertRequest(t.Context(), "")
		require.EqualError(t, err, "hostname is not valid")
	})
}

func TestRequestBYOHClientCertLabelsAndNamesTheRequest(t *testing.T) {
	localTestKey(t)

	client := fake.NewClientset()
	bcsr := &ByohCSR{
		bootstrapClient: client,
		logger:          logr.Discard(),
		expiryDuration:  24 * time.Hour,
	}

	created, err := bcsr.RequestBYOHClientCert(t.Context(), testHostName)
	require.NoError(t, err)

	other, err := bcsr.RequestBYOHClientCert(t.Context(), testHostName)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(created.Name, ByohCSRNamePrefix+testHostName+"-"), "the approver filters on this prefix and a denied request must not be reattached to on the next attempt")
	assert.Len(t, created.Name, len(ByohCSRNamePrefix)+len(testHostName)+1+csrNameSuffixLength)
	assert.NotEqual(t, created.Name, other.Name, "a denied request must not be reattached to on the next attempt")

	assert.Equal(t, hostCSRLabelValue(testHostName), created.Labels[infrastructurev1beta1.HostCSRLabel])
	assert.Equal(t, certv1.KubeAPIServerClientSignerName, created.Spec.SignerName)
	assert.Equal(t, []certv1.KeyUsage{certv1.UsageClientAuth}, created.Spec.Usages)
	require.NotNil(t, created.Spec.ExpirationSeconds)
	assert.Equal(t, int32((24 * time.Hour).Seconds()), *created.Spec.ExpirationSeconds)

	block, _ := pem.Decode(created.Spec.Request)
	require.NotNil(t, block)
	parsed, err := x509.ParseCertificateRequest(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf(ByohCSRCNFormat, testHostName), parsed.Subject.CommonName)
	assert.Equal(t, []string{ByohCSROrg}, parsed.Subject.Organization)
}
