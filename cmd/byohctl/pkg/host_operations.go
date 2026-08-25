// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package pkg

import (
	"context"
	"fmt"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

type HostOperationType string

const (
	OperationDeauthorise  HostOperationType = "deauthorise"
	OperationDecommission HostOperationType = "decommission"
)

// HostIO abstracts the side-effecting operations PerformHostOperation
// needs (interactive prompts and Debian package purge) so tests can pass a
// stub without mutating package-level state.
type HostIO interface {
	Confirm(msg string) (bool, error)
	Purge() error
}

// DefaultHostIO wires HostIO to the real utils.AskBool and
// service.PurgeDebianPackage implementations for production callers.
type DefaultHostIO struct{}

func (DefaultHostIO) Confirm(msg string) (bool, error) { return utils.AskBool(msg) }
func (DefaultHostIO) Purge() error                     { return service.PurgeDebianPackage() }

// PerformHostOperation performs the common steps for host deauthorisation or decommissioning.
func PerformHostOperation(ctx context.Context, k8sClient *client.Client, io HostIO, operationType HostOperationType, namespace string, force bool) error {

	// Deauthorise and decommission host steps -
	// 1. Check if the host is already onboarded ( by checking the respective byohost object in the management cluster)
	// 2. If the host is onboarded - Check if machineRef is set to the byohost object; If not set, just delete the byohost object and exit
	// 3. Annonate the respective machine object with "cluster.x-k8s.io/delete-machine"="yes"
	// 4. Scale down the machine deployment by 1
	// 5. Wait for machineRef to be unset from the byohost object status field
	// Once the machienRef is unset, host is deauthorised
	// If the request is to decommission, delete the byohost object and run dpkg purge
	// 6. Delete the byohost object
	// 7. Run dpkg --purge byohost-agent

	utils.LogInfo("Performing %s operation for host in namespace %s", operationType, namespace)

	// 1. Check if byohost object exists
	byoHost, err := k8sClient.GetByoHostObject(namespace)
	if err != nil {
		fmt.Println("failed to get ByoHosts object from the management plane: " + err.Error())
		// There might be a chance that the byohost object is not present in the management cluster
		// If decommission, ask user to proceed with host cleanup or not, run dpkg purge if yes
		if operationType == OperationDecommission {
			if !force {
				// Ask user to proceed with host cleanup or not
				continueDecommission, err := io.Confirm("Do you want to proceed with host cleanup? (y/n)")
				if err != nil {
					return fmt.Errorf("failed to get user input: %v", err)
				}
				if !continueDecommission {
					utils.LogInfo("Host cleanup declined by user; skipping dpkg purge")
					return nil
				}
			} else {
				utils.LogInfo("--force set: proceeding with host cleanup despite unreachable management plane")
			}
			if err := io.Purge(); err != nil {
				return fmt.Errorf("failed to run dpkg purge: %v", err)
			}
			utils.LogSuccess("Successfully ran dpkg purge")
			return nil
		}

		// If its here, the operationType is deauthorise
		// For deathorise byoHost object must be present in the management cluster
		if force {
			utils.LogInfo("--force set: management plane unreachable or ByoHost missing; treating deauthorise as a no-op")
			return nil
		}
		return fmt.Errorf("Cannot proceed ahead with the deauthorisation. Either restart the pf9-byohost-agent service or decommission and re-onboard.")
	}

	utils.LogSuccess("Successfully retrieved ByoHosts object from the management plane")

	// 2. Check if machineRef is set to the byohost object
	if byoHost.Status.MachineRef == nil {
		// Host is not attached to any cluster
		// Delete the byohost object and run dpkg purge if decommission
		// If deauthorise, just return
		if operationType == OperationDecommission {
			utils.LogInfo("MachineRef is not set to the byohost object. Host is not part of any cluster. Deleting the byohost object and running dpkg purge.")
			return performHostDecommissionWithNoMachineRef(ctx, k8sClient, io, namespace)
		}
		return fmt.Errorf("machineRef is not set for the byohost object. This host is not part of the cluster. Cannot proceed ahead with de-auth")

		// We should return from here even if deauth or decommission
	}

	machineName := byoHost.Status.MachineRef.Name

	// Get the machine object ( unstructured )
	unstructuredMachineObj, err := k8sClient.GetUnstructuredMachineObject(ctx, namespace, machineName)
	if err != nil {
		return fmt.Errorf("failed to get machine object: %v", err)
	}

	// At this point, we know that the host is part of some cluster since the machineRef is set.
	// There must be respctive machine object in the cluster and the machine deployment must have replicas set and greater than or equal to 1

	// TODO: Right now considering there is only one machine deployment is associated with the cluster.
	// There might be a multiple machine deployments associated with the cluster.
	// So when doing de-auth, check if the node count in the workload cluster and stop the de-auth if that is last node.

	// Check machine deployment replica count. If it is 1, then warn and ask the user to continue de-uth or not.
	replicaCount, err := k8sClient.GetMachineDeploymentReplicaCount(ctx, unstructuredMachineObj, namespace)
	if err != nil {
		return fmt.Errorf("failed to get machine deployment replica count: %v", err)
	}

	if replicaCount == 1 {
		fmt.Println("Info: Machine deployment replica count is 1. This is the last node in the cluster.")

		// Ask user to continue de-auth or not
		continueDeauth, err := io.Confirm("Do you want to continue with de-auth? (y/n)")
		if err != nil {
			return fmt.Errorf("failed to get user input: %v", err)
		}
		if !continueDeauth {
			return fmt.Errorf("Info: De-auth cancelled by user.")
		}

		// Since this is the last machine in the cluster, annotate machine objects to exclude the node drain
		err = k8sClient.AnnotateMachineObject(ctx, unstructuredMachineObj, namespace, "machine.cluster.x-k8s.io/exclude-node-draining", "")
		if err != nil {
			return fmt.Errorf("failed to annotate the last machine object to be deauth: %v", err)
		}
	}

	// Get the fresh machine object from the server to get the updated machine object
	unstructuredMachineObj, err = k8sClient.GetUnstructuredMachineObject(ctx, namespace, machineName)
	if err != nil {
		return fmt.Errorf("failed to get machine object: %v", err)
	}

	// 3. Annonate the respective machine object with "cluster.x-k8s.io/delete-machine"="yes"
	err = k8sClient.AnnotateMachineObject(ctx, unstructuredMachineObj, namespace, "cluster.x-k8s.io/delete-machine", "yes")
	if err != nil {
		return fmt.Errorf("failed to annotate machine object: %v", err)
	}

	utils.LogSuccess("Successfully annotated machine object that needs to be removed from the cluster")

	// 4. Scale down the machine deployment by 1
	err = k8sClient.ScaleDownMachineDeployment(ctx, unstructuredMachineObj, namespace)
	if err != nil {
		return fmt.Errorf("failed to scale down machine deployment: %v", err)
	}

	utils.LogSuccess("Successfully scaled down machine deployment by 1")

	// 5. Wait for machineRef to be unset from the byohost object status field
	err = k8sClient.WaitForMachineRefToBeUnset(ctx, byoHost, namespace)
	if err != nil {
		return fmt.Errorf("failed to wait for machineRef to be unset: %v", err)
	}

	utils.LogSuccess("MachineRef successfully unset for the host")

	// If operation is decommission, delete the byohost object and run dpkg purge
	if operationType == OperationDecommission {
		return performHostDecommissionWithNoMachineRef(ctx, k8sClient, io, namespace)
	}

	return nil
}

// Helper function to consolidate decommissioning logic when no machineRef is set
func performHostDecommissionWithNoMachineRef(ctx context.Context, k8sClient *client.Client, io HostIO, namespace string) error {
	utils.LogInfo("Deleting ByoHosts object and running dpkg purge")

	if err := k8sClient.DeleteByoHostObject(namespace); err != nil {
		return fmt.Errorf("failed to delete ByoHosts object: %v", err)
	}

	utils.LogSuccess("Successfully deleted ByoHosts object")

	if err := io.Purge(); err != nil {
		return fmt.Errorf("failed to run dpkg purge: %v", err)
	}

	utils.LogSuccess("Successfully ran dpkg purge")

	return nil
}
