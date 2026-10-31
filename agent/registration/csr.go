// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/certificate/csr"
	"k8s.io/client-go/util/keyutil"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
)

const (
	KeySize = 2048
	// ExpirationSeconds defines the expiry time for Certificates
	// which is currently set to 1 year aligned with kubeadm defaults.
	ExpirationSeconds = 86400 * 365
	ByohCSROrg        = "byoh:hosts"
	// ByohCSRNameFormat takes the tenant, the host name and a random part that
	// is new for every CSR. The byoh-csr- prefix is what the approver filters on.
	ByohCSRNameFormat = "byoh-csr-%s-%s-%s"
	DefaultConfigPath = ".byoh/config"

	randomPartLength = 5
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
// and create a kubeconfig which will be used then by the host reconciler.
// Progress is recorded next to the kubeconfig, so an interrupted onboarding
// resumes instead of starting over. The identity in the certificate names the
// tenant and the normalized hostName. A non-empty existingIdentity is used when
// there is no state to resume, so renewing a certificate keeps the identity of
// the host.
func (bcsr *ByohCSR) BootstrapKubeconfig(ctx context.Context, tenant, hostName, existingIdentity string) error {
	if hostName == "" {
		return errors.New("hostname is not valid")
	}
	if tenant == "" {
		return errors.New("tenant is not valid")
	}
	tokenID, err := bootstrapTokenID(bcsr.bootstrapClientConfig)
	if err != nil {
		return err
	}
	dir := filepath.Dir(bcsr.configPath)
	st, keyData, resumed, err := bcsr.loadOrCreateState(dir, tenant, hostName, tokenID, existingIdentity)
	if err != nil {
		return err
	}
	privateKey, err := keyutil.ParsePrivateKeyPEM(keyData)
	if err != nil {
		return fmt.Errorf("invalid private key for certificate request: %w", err)
	}
	bcsr.PrivateKey = keyData

	// A renewal only happens when a certificate already exists, and then the
	// caller passes the identity from that certificate. First onboarding never
	// has one.
	renewal := existingIdentity != ""
	reqName, reqUID, err := bcsr.ensureCSR(ctx, dir, st, tenant, hostName, privateKey, resumed, renewal)
	if err != nil {
		return err
	}
	bcsr.logger.Info("CSR request ready", "csrName", reqName)
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

	return st.markIssued(dir)
}

// bootstrapTokenID returns the id half of the bearer token in the bootstrap
// client config. The secret half is dropped on purpose.
func bootstrapTokenID(config *restclient.Config) (string, error) {
	if config.BearerToken == "" {
		return "", errors.New("bootstrap kubeconfig has no bearer token")
	}
	if !bootstraputil.IsValidBootstrapToken(config.BearerToken) {
		return "", errors.New("bearer token in the bootstrap kubeconfig is not a bootstrap token of the form <id>.<secret>")
	}
	id, _, _ := strings.Cut(config.BearerToken, ".")
	return id, nil
}

// newCSRName returns a CSR name that has not been used before.
func newCSRName(tenant, hostName string) string {
	return fmt.Sprintf(ByohCSRNameFormat, tenant, hostName, utilrand.String(randomPartLength))
}

// newIdentity returns the certificate common name for one onboarding. The
// suffix is new for every onboarding, so every onboarding of a host gets its
// own identity and its own ByoHost name.
func newIdentity(tenant, hostName string) string {
	return infrastructurev1beta1.FormatHostIdentity(tenant, hostName, utilrand.String(randomPartLength))
}

// matches reports whether the identity in st is for this tenant and host
// name, so a changed --namespace or hostname never reuses an old identity.
func (st *State) matches(tenant, hostName string) error {
	stTenant, stHostName, _, err := infrastructurev1beta1.ParseHostIdentity(st.Identity)
	if err != nil {
		return fmt.Errorf("state has an unusable identity: %w", err)
	}
	if stTenant != tenant || stHostName != hostName {
		return fmt.Errorf("state belongs to tenant %q and host %q, not %q and %q", stTenant, stHostName, tenant, hostName)
	}
	return nil
}

// loadOrCreateState returns the state of the current onboarding and its
// private key. State left by another onboarding, or state that cannot be
// trusted, is deleted together with its key and replaced by a fresh one that
// uses existingIdentity, or a new identity when that is empty.
// resumed is true when existing state was reused.
func (bcsr *ByohCSR) loadOrCreateState(dir, tenant, hostName, tokenID, existingIdentity string) (_ *State, _ []byte, resumed bool, _ error) {
	keyPath := filepath.Join(dir, PrivateKeyFileName)
	var keyData []byte

	st, loadErr := loadState(dir)
	if loadErr == nil && st.BootstrapTokenID != tokenID {
		loadErr = fmt.Errorf("state belongs to bootstrap token %q, not %q", st.BootstrapTokenID, tokenID)
	}
	if loadErr == nil {
		loadErr = st.matches(tenant, hostName)
	}
	if loadErr == nil {
		keyData, loadErr = os.ReadFile(keyPath)
		if loadErr == nil {
			return st, keyData, true, nil
		}
	}

	bcsr.logger.Info("starting a fresh onboarding", "reason", loadErr.Error(), "stateDir", dir)
	if err := removeOnboardingFiles(dir); err != nil {
		return nil, nil, false, err
	}
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return nil, nil, false, fmt.Errorf("create state directory %q: %w", dir, err)
	}
	keyData, _, err := keyutil.LoadOrGenerateKeyFile(keyPath)
	if err != nil {
		return nil, nil, false, fmt.Errorf("generate private key %q: %w", keyPath, err)
	}
	identity := existingIdentity
	if identity == "" {
		identity = newIdentity(tenant, hostName)
	}
	st = &State{
		Version:          stateVersion,
		BootstrapTokenID: tokenID,
		Identity:         identity,
		Phase:            PhasePending,
		CSRName:          newCSRName(tenant, hostName),
	}
	if err := st.save(dir); err != nil {
		return nil, nil, false, err
	}
	return st, keyData, false, nil
}

// ensureCSR makes sure the CSR named in the state exists, creating a new one
// under a new name when the old one cannot be used. A state in the issued phase
// is a renewal: its CSR is never reused, and the state goes back to pending.
// The renewal argument says the same for a pending state: a crash after the
// kubeconfig was written but before the state was marked issued leaves the
// phase pending, and reusing that CSR would return the old certificate. The
// state is saved before a CSR is created, so a crash never leaves a CSR the
// state does not name.
func (bcsr *ByohCSR) ensureCSR(ctx context.Context, dir string, st *State, tenant, hostName string, privateKey any, resumed, renewal bool) (string, types.UID, error) {
	if resumed {
		reusable := false
		var uid types.UID
		if st.Phase == PhasePending && !renewal {
			var err error
			uid, reusable, err = bcsr.reusableCSR(ctx, st)
			if err != nil {
				return "", "", err
			}
		}
		if reusable {
			if st.CSRUID != string(uid) {
				st.CSRUID = string(uid)
				if err := st.save(dir); err != nil {
					return "", "", err
				}
			}
			return st.CSRName, uid, nil
		}
		st.CSRName = newCSRName(tenant, hostName)
		st.Phase = PhasePending
		st.CSRUID = ""
		if err := st.save(dir); err != nil {
			return "", "", err
		}
	}

	csrData, err := generateCSR(st.Identity, privateKey)
	if err != nil {
		return "", "", fmt.Errorf("error generating csr %s: %w", st.Identity, err)
	}
	certTimeToExpire := bcsr.expiryDuration
	bcsr.logger.Info("certTimeToExpire", "duration", certTimeToExpire)
	reqName, reqUID, err := csr.RequestCertificateWithContext(ctx, bcsr.bootstrapClient,
		csrData,
		st.CSRName,
		certv1.KubeAPIServerClientSignerName,
		&certTimeToExpire,
		[]certv1.KeyUsage{certv1.UsageClientAuth},
		privateKey)
	if err != nil {
		return "", "", fmt.Errorf("request certificate %q: %w", st.CSRName, err)
	}
	st.CSRUID = string(reqUID)
	if err := st.save(dir); err != nil {
		return "", "", err
	}
	return reqName, reqUID, nil
}

// reusableCSR reports whether the CSR named in the state can still be waited
// on: it exists, is the same object the agent created, and was not denied or
// failed. A state without a CSR name has nothing to reuse.
func (bcsr *ByohCSR) reusableCSR(ctx context.Context, st *State) (types.UID, bool, error) {
	if st.CSRName == "" {
		return "", false, nil
	}
	existing, err := bcsr.bootstrapClient.CertificatesV1().CertificateSigningRequests().Get(ctx, st.CSRName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		bcsr.logger.Info("CSR from the state no longer exists, creating a new one", "csrName", st.CSRName)
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get CSR %q: %w", st.CSRName, err)
	}
	if st.CSRUID != "" && string(existing.UID) != st.CSRUID {
		bcsr.logger.Info("CSR has a different UID than recorded, creating a new one", "csrName", st.CSRName)
		return "", false, nil
	}
	for _, condition := range existing.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.Type == certv1.CertificateDenied || condition.Type == certv1.CertificateFailed {
			bcsr.logger.Info("CSR was denied or failed, creating a new one", "csrName", st.CSRName, "condition", string(condition.Type))
			return "", false, nil
		}
	}
	return existing.UID, true, nil
}

func generateCSR(commonName string, privKey any) ([]byte, error) {
	// Generate a new *x509.CertificateRequest template
	csrTemplate := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   commonName,
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
