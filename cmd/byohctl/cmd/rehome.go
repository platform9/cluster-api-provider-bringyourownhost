// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/platform9/cluster-api-provider-bringyourownhost/agent/registration"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

var rehomeBootstrapKubeconfig string

var rehomeCmd = &cobra.Command{
	Use:   "rehome",
	Short: "Point an onboarded host at a new management cluster and keep its identity",
	Long: `Point an onboarded host at a new management cluster, for example after clusterctl move.
The host keeps its ByoHost name. Create a BootstrapKubeconfig for the host in the
new management cluster and pass its kubeconfig here.`,
	Example: `  sudo byohctl rehome --bootstrap-kubeconfig ./bootstrap-kubeconfig.yaml`,
	Run:     runRehome,
}

func init() {
	rootCmd.AddCommand(rehomeCmd)
	rehomeCmd.Flags().StringVar(&rehomeBootstrapKubeconfig, "bootstrap-kubeconfig", "", "Path to a bootstrap kubeconfig issued by the new management cluster")
	_ = rehomeCmd.MarkFlagRequired("bootstrap-kubeconfig")
}

func runRehome(_ *cobra.Command, _ []string) {
	out, err := service.RunWithStdout(service.Systemctl, service.SystemctlServiceExists...)
	if err != nil || !strings.Contains(out, service.ByohAgentServiceName) {
		utils.LogError("%s is not installed on this host; onboard it instead", service.ByohAgentServiceName)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		utils.LogError("Error getting user home directory: %v", err)
		os.Exit(1)
	}
	byohDir := filepath.Join(homeDir, service.ByohConfigDir)

	systemctl := func(action string) error {
		_, runErr := service.RunWithStdout(service.Systemctl, action, service.ByohAgentServiceName)
		return runErr
	}
	hostIdentity, err := rehome(byohDir, bootstrapAgentConfDir, rehomeBootstrapKubeconfig, systemctl)
	if err != nil {
		utils.LogError("Failed to rehome host: %v", err)
		os.Exit(1)
	}
	utils.LogSuccess("Agent restarted against the new management cluster as %s", hostIdentity)
}

// rehome swaps the bootstrap kubeconfig and keeps the host identity.
func rehome(byohDir, confDir, bootstrapKubeconfigPath string, systemctl func(action string) error) (string, error) {
	// identity comes from state.json, the old config is only a fallback
	config := &restclient.Config{}
	if loaded, loadErr := clientcmd.BuildConfigFromFlags("", filepath.Join(byohDir, "config")); loadErr == nil {
		config = loaded
	}
	hostIdentity, err := registration.ResolveIdentity(byohDir, config)
	if err != nil {
		return "", fmt.Errorf("get host identity: %w", err)
	}
	parsed, err := identity.Parse(hostIdentity)
	if err != nil {
		return "", fmt.Errorf("identity %q: %w", hostIdentity, err)
	}

	if err := systemctl("stop"); err != nil {
		return "", fmt.Errorf("stop %s: %w", service.ByohAgentServiceName, err)
	}
	if err := writeBootstrapCredential(byohDir, confDir, bootstrapKubeconfigPath, parsed.TenantNamespace); err != nil {
		return "", err
	}
	// write it before removing the old cert, so the agent keeps its name if this stops halfway
	rehomePath := filepath.Join(byohDir, registration.RehomeIdentityFileName)
	if err := os.WriteFile(rehomePath, []byte(hostIdentity+"\n"), 0o600); err != nil { //nolint:mnd // owner-only, like the other state files
		return "", fmt.Errorf("write %s: %w", rehomePath, err)
	}
	for _, name := range []string{"config", registration.PrivateKeyFileName, registration.StateFileName} {
		path := filepath.Join(byohDir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("remove %s: %w", path, err)
		}
	}
	if err := systemctl("start"); err != nil {
		return "", fmt.Errorf("start %s: %w", service.ByohAgentServiceName, err)
	}
	return hostIdentity, nil
}
