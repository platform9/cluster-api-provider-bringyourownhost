// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package bootstraptoken_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/bootstraptoken"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
)

const (
	validTokenID     = "abcdef"
	validTokenSecret = "0123456789abcdef"
	validToken       = validTokenID + "." + validTokenSecret
)

func TestGetTokenIDSecretFromBootstrapToken_ValidToken(t *testing.T) {
	id, secret, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(validToken)
	require.NoError(t, err)
	assert.Equal(t, validTokenID, id)
	assert.Equal(t, validTokenSecret, secret)
}

func TestGetTokenIDSecretFromBootstrapToken_MalformedToken(t *testing.T) {
	tests := []struct {
		name     string
		tokenStr string
	}{
		{"empty string", ""},
		{"missing separator", validTokenID + validTokenSecret},
		{"id too short", "abc." + validTokenSecret},
		{"secret too short", validTokenID + ".short"},
		{"uppercase characters", "ABCDEF." + validTokenSecret},
		{"extra segment", validToken + ".extra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, secret, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(tt.tokenStr)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.tokenStr)
			assert.Empty(t, id)
			assert.Empty(t, secret)
		})
	}
}

func TestGenerateSecretFromBootstrapToken_MalformedToken(t *testing.T) {
	secret, err := bootstraptoken.GenerateSecretFromBootstrapToken("not-a-token", time.Hour)
	require.Error(t, err)
	assert.Nil(t, secret)
}

func TestGenerateSecretFromBootstrapToken_ValidToken(t *testing.T) {
	before := time.Now().UTC()
	secret, err := bootstraptoken.GenerateSecretFromBootstrapToken(validToken, time.Hour)
	require.NoError(t, err)
	require.NotNil(t, secret)

	assert.Equal(t, bootstraputil.BootstrapTokenSecretName(validTokenID), secret.Name)
	assert.Equal(t, metav1.NamespaceSystem, secret.Namespace)
	assert.Equal(t, bootstrapapi.SecretTypeBootstrapToken, secret.Type)

	assert.Equal(t, validTokenID, string(secret.Data[bootstrapapi.BootstrapTokenIDKey]))
	assert.Equal(t, validTokenSecret, string(secret.Data[bootstrapapi.BootstrapTokenSecretKey]))
	assert.Equal(t, "true", string(secret.Data[bootstrapapi.BootstrapTokenUsageSigningKey]))
	assert.Equal(t, "true", string(secret.Data[bootstrapapi.BootstrapTokenUsageAuthentication]))
	assert.Equal(t, infrastructurev1beta1.BootstrapTokenDescription, string(secret.Data[bootstrapapi.BootstrapTokenDescriptionKey]))
	assert.Equal(t, infrastructurev1beta1.BootstrapTokenExtraGroups, string(secret.Data[bootstrapapi.BootstrapTokenExtraGroupsKey]))

	expiration, err := time.Parse(time.RFC3339, string(secret.Data[bootstrapapi.BootstrapTokenExpirationKey]))
	require.NoError(t, err)
	// RFC3339 truncates sub-second precision, so allow a small tolerance around before+ttl.
	assert.WithinDuration(t, before.Add(time.Hour), expiration, time.Second)
}

func TestGenerateBootstrapKubeconfigFromBootstrapToken_MalformedToken(t *testing.T) {
	bootstrapKubeconfig := &infrastructurev1beta1.BootstrapKubeconfig{
		Spec: infrastructurev1beta1.BootstrapKubeconfigSpec{
			APIServer:                "https://cluster-a.example.com:6443",
			InsecureSkipTLSVerify:    false,
			CertificateAuthorityData: "test-ca-data",
		},
	}

	cfg, err := bootstraptoken.GenerateBootstrapKubeconfigFromBootstrapToken("not-a-token", bootstrapKubeconfig)
	require.Error(t, err)
	assert.Nil(t, cfg)
}

func TestGenerateBootstrapKubeconfigFromBootstrapToken_ValidToken(t *testing.T) {
	bootstrapKubeconfig := &infrastructurev1beta1.BootstrapKubeconfig{
		Spec: infrastructurev1beta1.BootstrapKubeconfigSpec{
			APIServer:                "https://cluster-a.example.com:6443",
			InsecureSkipTLSVerify:    false,
			CertificateAuthorityData: "test-ca-data",
		},
	}

	cfg, err := bootstraptoken.GenerateBootstrapKubeconfigFromBootstrapToken(validToken, bootstrapKubeconfig)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, infrastructurev1beta1.DefaultContext, cfg.CurrentContext)

	cluster, ok := cfg.Clusters[infrastructurev1beta1.DefaultClusterName]
	require.True(t, ok)
	assert.Equal(t, bootstrapKubeconfig.Spec.APIServer, cluster.Server)
	assert.Equal(t, bootstrapKubeconfig.Spec.InsecureSkipTLSVerify, cluster.InsecureSkipTLSVerify)
	assert.Equal(t, bootstrapKubeconfig.Spec.CertificateAuthorityData, string(cluster.CertificateAuthorityData))

	authInfo, ok := cfg.AuthInfos[infrastructurev1beta1.DefaultAuth]
	require.True(t, ok)
	assert.Equal(t, validTokenID+"."+validTokenSecret, authInfo.Token)

	context, ok := cfg.Contexts[infrastructurev1beta1.DefaultContext]
	require.True(t, ok)
	assert.Equal(t, infrastructurev1beta1.DefaultClusterName, context.Cluster)
	assert.Equal(t, infrastructurev1beta1.DefaultAuth, context.AuthInfo)
	assert.Equal(t, infrastructurev1beta1.DefaultNamespace, context.Namespace)
}
