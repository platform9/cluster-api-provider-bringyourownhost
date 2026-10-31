// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/platform9/cluster-api-provider-bringyourownhost/agent/registration"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/klog/v2/klogr"
)

const (
	csrApprovedMsg = "approved"
	bootstrapToken = "abcdef.0123456789abcdef"
)

// simulateByoAdmissionController approves the host's CSR and issues a certificate
// for it, the way the ByoAdmission controller would. It sends exactly one value on
// the returned channel before it exits: nil once the certificate is issued, or the
// reason it gave up, so a spec can both wait for it and see what went wrong. A
// canceled context is one of those reasons, since it means the certificate was
// never issued.
func simulateByoAdmissionController(ctx context.Context, tenant, hostName, cert string) <-chan error {
	done := make(chan error, 1)

	go func() {
		defer GinkgoRecover()

		csrClient := k8sClientSet.CertificatesV1().CertificateSigningRequests()
		csrNamePrefix := fmt.Sprintf(registration.ByohCSRNameFormat, tenant, hostName, "")

		ticker := time.NewTicker(time.Millisecond * 100)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				done <- fmt.Errorf("gave up approving the CSR for %q: %w", hostName, ctx.Err())
				return
			case <-ticker.C:
			}

			// The CSR name has a random part, so find the CSR by its prefix. A failed
			// List or no match means the host has not created its CSR yet, so keep polling.
			csrList, err := csrClient.List(ctx, metav1.ListOptions{})
			if err != nil {
				continue
			}
			var byohCSR *certv1.CertificateSigningRequest
			for i := range csrList.Items {
				if strings.HasPrefix(csrList.Items[i].Name, csrNamePrefix) {
					byohCSR = &csrList.Items[i]
					break
				}
			}
			if byohCSR == nil {
				continue
			}
			csrName := byohCSR.Name
			byohCSR.Status.Conditions = append(byohCSR.Status.Conditions, certv1.CertificateSigningRequestCondition{
				Type:    certv1.CertificateApproved,
				Reason:  csrApprovedMsg,
				Message: csrApprovedMsg,
				Status:  corev1.ConditionTrue,
			})
			_, err = csrClient.UpdateApproval(ctx, csrName, byohCSR, metav1.UpdateOptions{})
			if err != nil {
				done <- fmt.Errorf("approve the CSR for %q: %w", hostName, err)
				return
			}

			byohCSR, err = csrClient.Get(ctx, csrName, metav1.GetOptions{})
			if err != nil {
				done <- fmt.Errorf("re-read the approved CSR for %q: %w", hostName, err)
				return
			}
			byohCSR.Status.Certificate = []byte(cert)
			_, err = csrClient.UpdateStatus(ctx, byohCSR, metav1.UpdateOptions{})
			if err != nil {
				done <- fmt.Errorf("issue the certificate for %q: %w", hostName, err)
				return
			}
			done <- nil
			return
		}
	}()

	return done
}

var _ = Describe("CSR Registration", func() {
	var (
		hostName           = "test-host"
		tenant             = "test-tenant"
		fileDir            string
		bootstrapCfg       *restclient.Config
		certExpiryDuration = int64((time.Hour * 24).Seconds())
	)
	Context("When bootstrap kubeconfig is provided", func() {
		testDatabootstrapValid := []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: VGVzdA==
    server: https://cluster-a.com
  name: cluster-a
contexts:
- context:
    cluster: cluster-a
    namespace: ns-a
    user: user-a
  name: context-a
current-context: context-a
users:
- name: user-a
  user:
    token: mytoken-a
`)
		testDatabootstrapInvalid := []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority: ca.crt
    server: https://cluster-a.com
  name: cluster-a
contexts:
- context:
    cluster: cluster-a
    namespace: ns-a
    user: user-a
  name: context-a
current-context: context-a
users:
- name: user-a
  user:
    token: mytoken-a
`)
		var testCert = `
-----BEGIN CERTIFICATE-----
MIIBvzCCAWWgAwIBAgIRAMd7Mz3fPrLm1aFUn02lLHowCgYIKoZIzj0EAwIwIzEh
MB8GA1UEAwwYazNzLWNsaWVudC1jYUAxNjE2NDMxOTU2MB4XDTIxMDQxOTIxNTMz
MFoXDTIyMDQxOTIxNTMzMFowMjEVMBMGA1UEChMMc3lzdGVtOm5vZGVzMRkwFwYD
VQQDExBzeXN0ZW06bm9kZTp0ZXN0MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE
Xd9aZm6nftepZpUwof9RSUZqZDgu7dplIiDt8nnhO5Bquy2jn7/AVx20xb0Xz0d2
XLn3nn5M+lR2p3NlZmqWHaNrMGkwDgYDVR0PAQH/BAQDAgWgMBMGA1UdJQQMMAoG
CCsGAQUFBwMBMAwGA1UdEwEB/wQCMAAwHwYDVR0jBBgwFoAU/fZa5enijRDB25DF
NT1/vPUy/hMwEwYDVR0RBAwwCoIIRE5TOnRlc3QwCgYIKoZIzj0EAwIDSAAwRQIg
b3JL5+Q3zgwFrciwfdgtrKv8MudlA0nu6EDQO7eaJbwCIQDegFyC4tjGPp/5JKqQ
kovW9X7Ook/tTW0HyX6D6HRciA==
-----END CERTIFICATE-----
						`
		BeforeEach(func(ctx SpecContext) {
			csrList, err := k8sClientSet.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{})
			Expect(err).ShouldNot(HaveOccurred())
			for _, csr := range csrList.Items {
				err = k8sClientSet.CertificatesV1().CertificateSigningRequests().Delete(ctx, csr.Name, metav1.DeleteOptions{})
				Expect(err).ShouldNot(HaveOccurred())
			}
			fileDir, err = os.MkdirTemp("", "bootstrap")
			Expect(err).ShouldNot(HaveOccurred())
			// The agent keeps its state next to the kubeconfig.
			registration.ConfigPath = filepath.Join(fileDir, "config")
			// The test API server authenticates with the client certificate in cfg;
			// the bearer token only gives the agent a bootstrap token id to record.
			bootstrapCfg = restclient.CopyConfig(cfg)
			bootstrapCfg.BearerToken = bootstrapToken
		})

		AfterEach(func() {
			err := os.RemoveAll(fileDir)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return error if hostname is invalid", func() {
			CSRRegistrar, err := registration.NewByohCSR(bootstrapCfg, logr.Discard(), certExpiryDuration)
			Expect(err).ShouldNot(HaveOccurred())
			err = CSRRegistrar.BootstrapKubeconfig(context.Background(), tenant, "", "")
			Expect(err).To(MatchError("hostname is not valid"))
		})
		It("should return client config if bootstrap kubeconfig is valid", func() {
			fileboot, err := os.CreateTemp(fileDir, "bootstrapkubeconfig")
			Expect(err).ShouldNot(HaveOccurred())
			err = os.WriteFile(fileboot.Name(), testDatabootstrapValid, os.FileMode(0755))
			Expect(err).ShouldNot(HaveOccurred())
			restConfig, err := registration.LoadRESTClientConfig(fileboot.Name())
			Expect(err).ShouldNot(HaveOccurred())
			Expect(restConfig).ToNot(BeNil())
			Expect(restConfig.Host).To(Equal("https://cluster-a.com"))
		})
		It("should return error if bootstrap kubeconfig is invalid", func() {
			fileboot, err := os.CreateTemp(fileDir, "bootstrapkubeconfig")
			Expect(err).ShouldNot(HaveOccurred())
			err = os.WriteFile(fileboot.Name(), testDatabootstrapInvalid, os.FileMode(0755))
			Expect(err).ShouldNot(HaveOccurred())
			restConfig, err := registration.LoadRESTClientConfig(fileboot.Name())
			Expect(err).Should(HaveOccurred())
			Expect(restConfig).To(BeNil())
		})
		It("should create csr if bootstrap kubeconfig is valid", func(ctx SpecContext) {
			registration.CSRApprovalTimeout = time.Second * 2
			CSRRegistrar, err := registration.NewByohCSR(bootstrapCfg, logr.Discard(), certExpiryDuration)
			Expect(err).ShouldNot(HaveOccurred())
			// Nothing approves the CSR, so this times out after creating it.
			err = CSRRegistrar.BootstrapKubeconfig(ctx, tenant, hostName, "")
			Expect(err).Should(HaveOccurred())
			csrList, err := k8sClientSet.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(csrList.Items).To(HaveLen(1))
			ByohCSR := csrList.Items[0]
			Expect(ByohCSR.Name).To(HavePrefix(fmt.Sprintf(registration.ByohCSRNameFormat, tenant, hostName, "")))

			stateData, err := os.ReadFile(filepath.Join(fileDir, registration.StateFileName))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(string(stateData)).To(ContainSubstring(ByohCSR.Name))
			Expect(string(stateData)).To(ContainSubstring(string(ByohCSR.UID)))
			Expect(string(stateData)).ToNot(ContainSubstring(bootstrapToken))
			// Validate k8s CSR resource
			Expect(ByohCSR.Spec.SignerName).Should(Equal(certv1.KubeAPIServerClientSignerName))
			Expect(ByohCSR.Spec.Usages).Should(Equal([]certv1.KeyUsage{certv1.UsageClientAuth}))
			Expect(*ByohCSR.Spec.ExpirationSeconds).Should(Equal(int32((time.Hour * 24).Seconds())))
			// Validate Certificate Request
			pemData, _ := pem.Decode(ByohCSR.Spec.Request)
			Expect(pemData).ToNot(Equal(nil))
			csr, err := x509.ParseCertificateRequest(pemData.Bytes)
			Expect(err).ToNot(HaveOccurred())
			Expect(csr.Subject.CommonName).To(HavePrefix(fmt.Sprintf("byoh:host:%s:%s-", tenant, hostName)))
			Expect(string(stateData)).To(ContainSubstring(csr.Subject.CommonName))
			Expect(csr.Subject.Organization[0]).To(Equal("byoh:hosts"))

			keyInfo, err := os.Stat(filepath.Join(fileDir, registration.PrivateKeyFileName))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(keyInfo.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		})
		It("should timeout if the CSR is not approved", func(ctx SpecContext) {
			registration.CSRApprovalTimeout = time.Second * 5
			CSRRegistrar, err := registration.NewByohCSR(bootstrapCfg, klogr.New(), certExpiryDuration) //nolint: staticcheck // klogr predates the textlogger migration; see main.go
			Expect(err).ShouldNot(HaveOccurred())
			err = CSRRegistrar.BootstrapKubeconfig(ctx, tenant, hostName, "")
			Expect(err).Should(HaveOccurred())
			Expect(err).To(MatchError("timed out waiting for the condition"))
		})
		It("should return error if not able to write kubeconfig", func(ctx SpecContext) {
			admissionDone := simulateByoAdmissionController(ctx, tenant, hostName, testCert)
			defer func() {
				var admissionErr error
				Eventually(admissionDone, time.Second*5).Should(Receive(&admissionErr))
				Expect(admissionErr).ShouldNot(HaveOccurred())
			}()
			// A directory where the kubeconfig file should go deterministically fails
			// writing the kubeconfig regardless of whether the test process is running
			// as root or not (unlike a permission-denied path, which root would bypass).
			err := os.Mkdir(registration.ConfigPath, 0o755)
			Expect(err).ShouldNot(HaveOccurred())
			CSRRegistrar, err := registration.NewByohCSR(bootstrapCfg, klogr.New(), certExpiryDuration) //nolint: staticcheck // klogr predates the textlogger migration; see main.go
			Expect(err).ShouldNot(HaveOccurred())
			err = CSRRegistrar.BootstrapKubeconfig(ctx, tenant, hostName, "")
			Expect(err).Should(HaveOccurred())
			Expect(err).To(MatchError(fmt.Sprintf("open %s: is a directory", registration.ConfigPath)))
		})
		It("should create kubeconfig if csr is approved", func(ctx SpecContext) {
			admissionDone := simulateByoAdmissionController(ctx, tenant, hostName, testCert)
			defer func() {
				var admissionErr error
				Eventually(admissionDone, time.Second*5).Should(Receive(&admissionErr))
				Expect(admissionErr).ShouldNot(HaveOccurred())
			}()
			CSRRegistrar, err := registration.NewByohCSR(bootstrapCfg, klogr.New(), certExpiryDuration) //nolint: staticcheck // klogr predates the textlogger migration; see main.go
			Expect(err).ShouldNot(HaveOccurred())
			err = CSRRegistrar.BootstrapKubeconfig(ctx, tenant, hostName, "")
			Expect(err).ShouldNot(HaveOccurred())
			Expect(registration.ConfigPath).To(BeARegularFile())
			Expect(os.Remove(registration.ConfigPath)).ShouldNot(HaveOccurred())
		})
	})
	Context("When GetBYOHConfigPath is called", func() {
		homePath := os.Getenv("HOME")
		BeforeEach(func() {
			registration.ConfigPath = ""
		})
		AfterEach(func() {
			os.Setenv("HOME", homePath)
		})
		It("should return ConfigPath if set", func() {
			registration.ConfigPath = "/tmp/config"
			actualPath := registration.GetBYOHConfigPath()
			Expect(actualPath).To(Equal(registration.ConfigPath))
		})
		It("should return default path if homedir is not set", func() {
			os.Setenv("HOME", "")
			actualPath := registration.GetBYOHConfigPath()
			Expect(actualPath).To(Equal(".byoh/config"))
		})
		It("should return path under user home dir if ConfigPath is not set", func() {
			homeDir, err := os.UserHomeDir()
			Expect(err).ShouldNot(HaveOccurred())
			expectedPath := filepath.Join(homeDir, registration.DefaultConfigPath)
			actualPath := registration.GetBYOHConfigPath()
			Expect(actualPath).To(Equal(expectedPath))
		})
	})
})
