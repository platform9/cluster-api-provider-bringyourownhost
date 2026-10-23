// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"

	infraproviderv1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// byoHostLogCollector collects machine logs from the BYO host container backing each Machine.
//
// framework.DockerLogCollector assumes CAPD's naming, where a Machine's container is named
// <cluster>-<machine>. BYO host containers are named after the host instead, so used directly it
// execs into containers that don't exist and writes every log file empty.
type byoHostLogCollector struct {
	framework.DockerLogCollector
}

func (c byoHostLogCollector) CollectMachineLog(ctx context.Context, mgmtClient client.Client, m *clusterv1.Machine, outputPath string) error {
	// The attach label is set when a host is picked for the ByoMachine, before kubeadm runs, so
	// this also finds the host of a Machine whose node never joined.
	hosts := &infraproviderv1.ByoHostList{}
	if err := mgmtClient.List(ctx, hosts, client.InNamespace(m.Namespace), client.MatchingLabels{
		infraproviderv1.AttachedByoMachineLabel: m.Namespace + "." + m.Spec.InfrastructureRef.Name,
	}); err != nil {
		return fmt.Errorf("listing ByoHosts attached to Machine %s/%s: %w", m.Namespace, m.Name, err)
	}
	if len(hosts.Items) == 0 {
		return fmt.Errorf("no ByoHost attached to Machine %s/%s", m.Namespace, m.Name)
	}

	// The ByoHost is named after its container's hostname, which is also the container name. With
	// an empty cluster name, DockerLogCollector uses the Machine name as the container name as is.
	hostMachine := m.DeepCopy()
	hostMachine.Name = hosts.Items[0].Name
	hostMachine.Spec.ClusterName = ""
	return c.DockerLogCollector.CollectMachineLog(ctx, mgmtClient, hostMachine, outputPath)
}

// CollectInfrastructureLogs is a no-op: BYOH clusters have no load balancer container (the control
// plane endpoint is served by kube-vip on the hosts), which is all DockerLogCollector collects here.
func (c byoHostLogCollector) CollectInfrastructureLogs(_ context.Context, _ client.Client, _ *clusterv1.Cluster, _ string) error {
	return nil
}
