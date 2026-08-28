// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	certv1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/certificate/csr"
	"k8s.io/client-go/util/keyutil"
)

const (
	KeySize = 2048
	// ExpirationSeconds defines the expiry time for Certificates
	// which is currently set to 1 year aligned with kubeadm defaults.
	ExpirationSeconds = 86400 * 365
	ByohCSROrg        = "byoh:hosts"
	ByohCSRCNFormat   = "byoh:host:%s"
	// ByohCSRNamePrefix is the prefix the ByoAdmission controller filters on,
	// so every request the agent creates has to carry it.
	ByohCSRNamePrefix = "byoh-csr-"
	// ByohCSRNameFormat names a request after the host and a random suffix.
	// The suffix is what lets a host walk away from a denied request: the
	// agent has no permission to delete one, so it has to create a
	// differently named replacement instead.
	ByohCSRNameFormat = ByohCSRNamePrefix + "%s-%s"
	TmpPrivateKey     = "byoh-client.key.tmp"
	DefaultConfigPath = ".byoh/config"

	// csrNameSuffixLength is how many random characters newCSRName appends.
	csrNameSuffixLength = 5
)

// csrState describes what the agent found on the API server for a host.
type csrState string

const (
	// csrStateNone means the host has no certificate signing request.
	csrStateNone csrState = "none"
	// csrStatePending means a request is still waiting for the approver.
	csrStatePending csrState = "pending"
	// csrStateApproved means the approver accepted a request, so its
	// certificate is either already issued or about to be.
	csrStateApproved csrState = "approved"
	// csrStateRejected means every request the host has was denied or
	// failed. None of them can ever yield a certificate.
	csrStateRejected csrState = "rejected"
)

var (
	ConfigPath string
	// CSRApprovalTimeout defines the time to wait for certificate to
	// be issued. Currently set to 1 hour.
	CSRApprovalTimeout = 3600 * time.Second
)

type ByohCSR struct {
	bootstrapClientConfig *restclient.Config
	bootstrapClient       clientset.Interface
	PrivateKey            []byte
	configPath            string
	logger                logr.Logger
	expiryDuration        time.Duration
}

// NewByohCSR returns a ByohCSR instance
func NewByohCSR(bootstrapClientConfig *restclient.Config, logger logr.Logger, expiryDurationInSeconds int64) (*ByohCSR, error) {
	bootstrapClient, err := clientset.NewForConfig(bootstrapClientConfig)
	if err != nil {
		return nil, err
	}
	return &ByohCSR{
		bootstrapClientConfig: bootstrapClientConfig,
		bootstrapClient:       bootstrapClient,
		configPath:            GetBYOHConfigPath(),
		logger:                logger,
		expiryDuration:        time.Duration(expiryDurationInSeconds) * time.Second,
	}, nil
}

// BootstrapKubeconfig will create a CertificateSigningRequest for the host
// its running on and once the CSR is approved it will fetch the Certificate
// and create a kubeconfig which will be used then by the host reconciler
func (bcsr *ByohCSR) BootstrapKubeconfig(ctx context.Context, hostName string) error {
	reqName, reqUID, err := bcsr.ensureClientCertRequest(ctx, hostName)
	if err != nil {
		return err
	}
	bcsr.logger.Info("CSR request created", "name", reqName)
	// wait for certificate to be issued
	ctx, cancel := context.WithTimeout(ctx, CSRApprovalTimeout)
	defer cancel()
	bcsr.logger.Info("waiting for client certificate to be issued")
	certData, err := csr.WaitForCertificate(ctx, bcsr.bootstrapClient, reqName, reqUID)
	if err != nil {
		return err
	}
	err = writeKubeconfigFromBootstrapping(bcsr.bootstrapClientConfig, bcsr.configPath, certData, bcsr.PrivateKey)
	if err != nil {
		return err
	}
	bcsr.logger.Info("kubeconfig created", "path", bcsr.configPath)
	if err := os.Remove(TmpPrivateKey); err != nil && !os.IsNotExist(err) {
		bcsr.logger.Error(err, "Failed cleaning up private key file")
	}
	return nil
}

// ensureClientCertRequest returns the certificate signing request this host
// should wait on. It reuses one the host already has when that request can
// still produce a usable certificate, and creates a fresh one otherwise. A
// denied or failed request always leads to a new one, since the agent cannot
// delete the old object and would otherwise re-attach to it on every restart.
func (bcsr *ByohCSR) ensureClientCertRequest(ctx context.Context, hostname string) (string, types.UID, error) {
	if hostname == "" {
		return "", "", errors.New("hostname is not valid")
	}

	existing, err := bcsr.listHostCSRs(ctx, hostname)
	if err != nil {
		return "", "", fmt.Errorf("list certificate signing requests for host %q: %w", hostname, err)
	}

	req, state := selectCSR(existing)
	bcsr.logger.Info("looked up certificate signing requests for this host", "found", len(existing), "state", string(state))

	if (state == csrStateApproved || state == csrStatePending) && bcsr.canReuse(req) {
		bcsr.logger.Info("reusing an existing certificate signing request", "name", req.Name, "state", string(state))
		return req.Name, req.UID, nil
	}

	return bcsr.RequestBYOHClientCert(ctx, hostname)
}

// listHostCSRs returns every certificate signing request labeled for
// hostname.
func (bcsr *ByohCSR) listHostCSRs(ctx context.Context, hostname string) ([]certv1.CertificateSigningRequest, error) {
	selector := labels.SelectorFromSet(labels.Set{
		infrastructurev1beta1.HostCSRLabel: hostCSRLabelValue(hostname),
	})
	list, err := bcsr.bootstrapClient.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// canReuse reports whether the agent can wait on req. A certificate is only
// usable if req was made with the private key still on this host, so a
// request left behind by an earlier key has to be abandoned. When the answer
// is yes, that key becomes the one the kubeconfig is written with.
func (bcsr *ByohCSR) canReuse(req *certv1.CertificateSigningRequest) bool {
	keyData, _, err := keyutil.LoadOrGenerateKeyFile(TmpPrivateKey)
	if err != nil {
		bcsr.logger.Error(err, "could not load the local private key")
		return false
	}
	privateKey, err := keyutil.ParsePrivateKeyPEM(keyData)
	if err != nil {
		bcsr.logger.Error(err, "could not parse the local private key")
		return false
	}
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		bcsr.logger.Info("the local private key cannot sign, ignoring the existing certificate signing request", "name", req.Name)
		return false
	}
	matches, err := requestMatchesKey(req, signer)
	if err != nil {
		bcsr.logger.Error(err, "could not compare the existing certificate signing request with the local private key", "name", req.Name)
		return false
	}
	if !matches {
		bcsr.logger.Info("the existing certificate signing request was made with a different private key", "name", req.Name)
		return false
	}

	bcsr.PrivateKey = keyData
	return true
}

// selectCSR picks the request the agent should act on out of the ones
// labeled for one host, and reports its state. An approved request wins
// over a pending one, and both win over a denied or failed one, so a host
// that accumulated several attempts still converges on the usable request.
func selectCSR(items []certv1.CertificateSigningRequest) (*certv1.CertificateSigningRequest, csrState) {
	var pending *certv1.CertificateSigningRequest

	for i := range items {
		switch requestState(&items[i]) {
		case csrStateApproved:
			return &items[i], csrStateApproved
		case csrStatePending:
			if pending == nil {
				pending = &items[i]
			}
		case csrStateNone, csrStateRejected:
		}
	}

	if pending != nil {
		return pending, csrStatePending
	}
	if len(items) > 0 {
		return nil, csrStateRejected
	}
	return nil, csrStateNone
}

// requestState reports whether one request is approved, denied or failed, or
// still waiting. It reads only the condition type, which is what
// csr.WaitForCertificate does, so the agent and the wait it is about to
// start never disagree about the same object.
func requestState(req *certv1.CertificateSigningRequest) csrState {
	approved := false
	for _, condition := range req.Status.Conditions {
		switch condition.Type {
		case certv1.CertificateDenied, certv1.CertificateFailed:
			return csrStateRejected
		case certv1.CertificateApproved:
			approved = true
		}
	}
	if approved {
		return csrStateApproved
	}
	return csrStatePending
}

// requestMatchesKey reports whether req was created from privateKey.
func requestMatchesKey(req *certv1.CertificateSigningRequest, privateKey crypto.Signer) (bool, error) {
	block, _ := pem.Decode(req.Spec.Request)
	if block == nil {
		return false, fmt.Errorf("certificate signing request %q holds no PEM data", req.Name)
	}
	parsed, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return false, fmt.Errorf("parse certificate signing request %q: %w", req.Name, err)
	}
	requested, err := x509.MarshalPKIXPublicKey(parsed.PublicKey)
	if err != nil {
		return false, fmt.Errorf("marshal the public key of certificate signing request %q: %w", req.Name, err)
	}
	local, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		return false, fmt.Errorf("marshal the public key of the local private key: %w", err)
	}
	return bytes.Equal(requested, local), nil
}

// newCSRName returns a name for a new certificate signing request. The
// random suffix is what makes each attempt a distinct object.
func newCSRName(hostname string) string {
	return fmt.Sprintf(ByohCSRNameFormat, hostname, utilrand.String(csrNameSuffixLength))
}

// hostCSRLabelValue turns hostname into something usable as a label value.
// Label values stop at 63 characters while a host name can be longer, so an
// over-long name keeps a readable prefix plus a hash of the whole thing. The
// result is deterministic, which is what lets the agent find its own request
// again after a restart.
func hostCSRLabelValue(hostname string) string {
	if len(hostname) <= infrastructurev1beta1.MaxK8sLabelValueLength {
		return hostname
	}
	sum := sha256.Sum256([]byte(hostname))
	suffix := hex.EncodeToString(sum[:])[:infrastructurev1beta1.LabelHashLength]
	return hostname[:infrastructurev1beta1.MaxLabelPrefixLength] + infrastructurev1beta1.LabelSeparator + suffix
}

// RequestBYOHClientCert will generate Private Key and then will create a
// CertificateSigningRequest in K8s
func (bcsr *ByohCSR) RequestBYOHClientCert(ctx context.Context, hostname string) (string, types.UID, error) {
	if hostname == "" {
		return "", "", errors.New("hostname is not valid")
	}
	keyData, _, err := keyutil.LoadOrGenerateKeyFile(TmpPrivateKey)
	if err != nil {
		return "", "", err
	}
	privateKey, err := keyutil.ParsePrivateKeyPEM(keyData)
	if err != nil {
		return "", "", fmt.Errorf("invalid private key for certificate request: %w", err)
	}
	bcsr.PrivateKey = keyData
	csrData, err := generateCSR(hostname, privateKey)
	if err != nil {
		return "", "", fmt.Errorf("error generating csr %s, err=%w", hostname, err)
	}
	bcsr.logger.Info("certTimeToExpire", "duration", bcsr.expiryDuration)

	req := &certv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: newCSRName(hostname),
			Labels: map[string]string{
				infrastructurev1beta1.HostCSRLabel: hostCSRLabelValue(hostname),
			},
		},
		Spec: certv1.CertificateSigningRequestSpec{
			Request:           csrData,
			SignerName:        certv1.KubeAPIServerClientSignerName,
			ExpirationSeconds: csr.DurationToExpirationSeconds(bcsr.expiryDuration),
			Usages:            []certv1.KeyUsage{certv1.UsageClientAuth},
		},
	}

	created, err := bcsr.bootstrapClient.CertificatesV1().CertificateSigningRequests().Create(ctx, req, metav1.CreateOptions{})
	if err != nil {
		return "", "", fmt.Errorf("create certificate signing request %q: %w", req.Name, err)
	}
	return created.Name, created.UID, nil
}

func generateCSR(hostname string, privKey interface{}) ([]byte, error) {
	// Generate a new *x509.CertificateRequest template
	csrTemplate := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf(ByohCSRCNFormat, hostname),
			Organization: []string{ByohCSROrg},
		},
	}
	// Generate the CSR bytes
	csrData, err := x509.CreateCertificateRequest(rand.Reader, &csrTemplate, privKey)
	if err != nil {
		return nil, err
	}
	csrPemBlock := &pem.Block{
		Type:  cert.CertificateRequestBlockType,
		Bytes: csrData,
	}
	return pem.EncodeToMemory(csrPemBlock), nil
}

// LoadRESTClientConfig is to create an instance of *restclient.Config from
// the boostrap kubeconfig path, this then will be used to create bootstrap
// k8s client
func LoadRESTClientConfig(kubeconfigPath string) (*restclient.Config, error) {
	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	loadedConfig, err := loader.Load()
	if err != nil {
		return nil, err
	}
	// Flatten the loaded data to a particular restclient.Config based on the current context.
	return clientcmd.NewNonInteractiveClientConfig(
		*loadedConfig,
		loadedConfig.CurrentContext,
		&clientcmd.ConfigOverrides{},
		loader,
	).ClientConfig()
}

// writeKubeconfigFromBootstrapping will write the new kubeconfig fetching
// some details from bootstrap client config and using key/cert details
func writeKubeconfigFromBootstrapping(bootstrapClientConfig *restclient.Config, kubeconfigPath string, certData, keyData []byte) error {
	// Get the CA data from the bootstrap client config.
	caFile, caData := bootstrapClientConfig.CAFile, []byte{}
	if caFile == "" {
		caData = bootstrapClientConfig.CAData
	}

	// Build resulting kubeconfig.
	kubeconfigData := clientcmdapi.Config{
		// Define a cluster stanza based on the bootstrap kubeconfig.
		Clusters: map[string]*clientcmdapi.Cluster{"default-cluster": {
			Server:                   bootstrapClientConfig.Host,
			InsecureSkipTLSVerify:    bootstrapClientConfig.Insecure,
			CertificateAuthority:     caFile,
			CertificateAuthorityData: caData,
		}},
		// Define auth based on the obtained client cert.
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"default-auth": {
			ClientCertificateData: certData,
			ClientKeyData:         keyData,
		}},
		// Define a context that connects the auth info and cluster, and set it as the default
		Contexts: map[string]*clientcmdapi.Context{"default-context": {
			Cluster:   "default-cluster",
			AuthInfo:  "default-auth",
			Namespace: "default",
		}},
		CurrentContext: "default-context",
	}

	// Marshal to disk
	return clientcmd.WriteToFile(kubeconfigData, kubeconfigPath)
}

// GetBYOHConfigPath set the directory for BYOH kubeconfig
func GetBYOHConfigPath() string {
	if ConfigPath != "" {
		return ConfigPath
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return DefaultConfigPath
	}
	return filepath.Join(homeDir, DefaultConfigPath)
}
