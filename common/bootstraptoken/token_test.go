// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package bootstraptoken_test

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/bootstraptoken"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
)

func TestBootstrapToken(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "BootstrapToken Suite")
}

const (
	validTokenID     = "abcdef"
	validTokenSecret = "0123456789abcdef"
	validToken       = validTokenID + "." + validTokenSecret
)

var _ = Describe("GetTokenIDSecretFromBootstrapToken", func() {
	It("splits a well-formed token into id and secret", func() {
		id, secret, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(validToken)
		Expect(err).NotTo(HaveOccurred())
		Expect(id).To(Equal(validTokenID))
		Expect(secret).To(Equal(validTokenSecret))
	})

	DescribeTable("rejects malformed tokens",
		func(tokenStr string) {
			id, secret, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(tokenStr)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(tokenStr))
			Expect(id).To(BeEmpty())
			Expect(secret).To(BeEmpty())
		},
		Entry("empty string", ""),
		Entry("missing separator", validTokenID+validTokenSecret),
		Entry("id too short", "abc."+validTokenSecret),
		Entry("secret too short", validTokenID+".short"),
		Entry("uppercase characters", "ABCDEF."+validTokenSecret),
		Entry("extra segment", validToken+".extra"),
	)
})

var _ = Describe("GenerateSecretFromBootstrapToken", func() {
	It("returns an error when the token is malformed", func() {
		secret, err := bootstraptoken.GenerateSecretFromBootstrapToken("not-a-token", time.Hour)
		Expect(err).To(HaveOccurred())
		Expect(secret).To(BeNil())
	})

	It("builds a well-formed bootstrap token secret", func() {
		before := time.Now().UTC()
		secret, err := bootstraptoken.GenerateSecretFromBootstrapToken(validToken, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(secret).NotTo(BeNil())

		Expect(secret.Name).To(Equal(bootstraputil.BootstrapTokenSecretName(validTokenID)))
		Expect(secret.Namespace).To(Equal(metav1.NamespaceSystem))
		Expect(secret.Type).To(Equal(bootstrapapi.SecretTypeBootstrapToken))

		Expect(string(secret.Data[bootstrapapi.BootstrapTokenIDKey])).To(Equal(validTokenID))
		Expect(string(secret.Data[bootstrapapi.BootstrapTokenSecretKey])).To(Equal(validTokenSecret))
		Expect(string(secret.Data[bootstrapapi.BootstrapTokenUsageSigningKey])).To(Equal("true"))
		Expect(string(secret.Data[bootstrapapi.BootstrapTokenUsageAuthentication])).To(Equal("true"))
		Expect(string(secret.Data[bootstrapapi.BootstrapTokenDescriptionKey])).To(Equal(infrastructurev1beta1.BootstrapTokenDescription))
		Expect(string(secret.Data[bootstrapapi.BootstrapTokenExtraGroupsKey])).To(Equal(infrastructurev1beta1.BootstrapTokenExtraGroups))

		expiration, err := time.Parse(time.RFC3339, string(secret.Data[bootstrapapi.BootstrapTokenExpirationKey]))
		Expect(err).NotTo(HaveOccurred())
		// RFC3339 truncates sub-second precision, so allow a small tolerance around before+ttl.
		Expect(expiration).To(BeTemporally("~", before.Add(time.Hour), time.Second))
	})
})

var _ = Describe("GenerateBootstrapKubeconfigFromBootstrapToken", func() {
	bootstrapKubeconfig := &infrastructurev1beta1.BootstrapKubeconfig{
		Spec: infrastructurev1beta1.BootstrapKubeconfigSpec{
			APIServer:                "https://cluster-a.example.com:6443",
			InsecureSkipTLSVerify:    false,
			CertificateAuthorityData: "test-ca-data",
		},
	}

	It("returns an error when the token is malformed", func() {
		cfg, err := bootstraptoken.GenerateBootstrapKubeconfigFromBootstrapToken("not-a-token", bootstrapKubeconfig)
		Expect(err).To(HaveOccurred())
		Expect(cfg).To(BeNil())
	})

	It("builds a kubeconfig wired to the default cluster/context/auth", func() {
		cfg, err := bootstraptoken.GenerateBootstrapKubeconfigFromBootstrapToken(validToken, bootstrapKubeconfig)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())

		Expect(cfg.CurrentContext).To(Equal(infrastructurev1beta1.DefaultContext))

		cluster, ok := cfg.Clusters[infrastructurev1beta1.DefaultClusterName]
		Expect(ok).To(BeTrue())
		Expect(cluster.Server).To(Equal(bootstrapKubeconfig.Spec.APIServer))
		Expect(cluster.InsecureSkipTLSVerify).To(Equal(bootstrapKubeconfig.Spec.InsecureSkipTLSVerify))
		Expect(string(cluster.CertificateAuthorityData)).To(Equal(bootstrapKubeconfig.Spec.CertificateAuthorityData))

		authInfo, ok := cfg.AuthInfos[infrastructurev1beta1.DefaultAuth]
		Expect(ok).To(BeTrue())
		Expect(authInfo.Token).To(Equal(validTokenID + "." + validTokenSecret))

		context, ok := cfg.Contexts[infrastructurev1beta1.DefaultContext]
		Expect(ok).To(BeTrue())
		Expect(context.Cluster).To(Equal(infrastructurev1beta1.DefaultClusterName))
		Expect(context.AuthInfo).To(Equal(infrastructurev1beta1.DefaultAuth))
		Expect(context.Namespace).To(Equal(infrastructurev1beta1.DefaultNamespace))
	})
})
