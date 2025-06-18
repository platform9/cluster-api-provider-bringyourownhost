package pkg

import (
	"fmt"
	"os"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

type HostOperationType string

const (
	OperationDeauthorise  HostOperationType = "deauthorise"
	OperationDecommission HostOperationType = "decommission"
)

// PerformHostOperation performs the common steps for host deauthorisation or decommissioning
func PerformHostOperation(operationType HostOperationType, namespace string) error {

	// Deauthorise and decommission host steps -
	// 1. Authenticate with Platform9 with the kubeconfig present in the agent directory ( kubeconfig )
	// 2. Check if the host is already onboarded ( by checking the respective byohost object in the management cluster)
	// 3. If the host is onboarded - Check if machineRef is set to the byohost object; If not set, just delete the byohost object and exit
	// 4. Annonate the respective machine object with "cluster.x-k8s.io/delete-machine"="yes"
	// 5. Scale down the machine deployment by 1
	// 6. Wait for machineRef to be unset from the byohost object status field
	// Once the machienRef is unset, host is deauthorised
	// If the request is to decommission, delete the byohost object and run dpkg purge
	// 7. Delete the byohost object
	// 8. Run dpkg --purge byohost-agent

	utils.LogInfo("Performing %s operation for host in namespace %s", operationType, namespace)

	// 1. Check if kubeconfig file exists
	if _, err := os.Stat(service.KubeconfigFilePath); os.IsNotExist(err) {
		return fmt.Errorf("kubeconfig file not found at %s. Please onboard the host first.", service.KubeconfigFilePath)
	}

	// 2. Get Kubernetes client
	client, err := client.GetK8sClient(service.KubeconfigFilePath)
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %v", err)
	}

	utils.LogSuccess("Successfully retrieved Kubernetes client")

	// 3. Check if byohost object exists
	byoHost, err := client.GetByoHostObject(namespace)
	if err != nil {
		fmt.Println("failed to get ByoHosts object from the management plane: " + err.Error())
		// There might be a chance that the byohost object is not present in the management cluster
		// If decommission, ask user to proceed with host cleanup or not, run dpkg purge if yes
		if operationType == OperationDecommission {
			// Ask user to proceed with host cleanup or not
			continueDecommission, err := utils.AskBool("Do you want to proceed with host cleanup? (y/n)")
			if err != nil {
				return fmt.Errorf("failed to get user input: %v", err)
			}
			if !continueDecommission {
				return nil
			}
			err = service.PurgeDebianPackage()
			if err != nil {
				return fmt.Errorf("failed to run dpkg purge: %v", err)
			}
			utils.LogSuccess("Successfully ran dpkg purge")
			return nil
		}

		// If its here, the operationType is deauthorise
		// For deathorise byoHost object must be present in the management cluster
		return fmt.Errorf("Cannot proceed ahead with the deauthorisation. Either restart the pf9-byohost-agent service or decommission and re-onboard.")
	}

	utils.LogSuccess("Successfully retrieved ByoHosts object from the management plane")

	// 4. Check if machineRef is set to the byohost object
	if byoHost.Status.MachineRef == nil {
		// Host is not attached to any cluster
		// Delete the byohost object and run dpkg purge if decommission
		// If deauthorise, just return
		if operationType == OperationDecommission {
			utils.LogInfo("MachineRef is not set to the byohost object. Host is not part of any cluster. Deleting the byohost object and running dpkg purge.")
			return performHostDecommissionWithNoMachineRef(client, namespace)
		}
		return fmt.Errorf("machineRef is not set for the byohost object. This host is not part of the cluster. Cannot proceed ahead with de-auth")

		// We should return from here even if deauth or decommission
	}

	machineName := byoHost.Status.MachineRef.Name

	// Get the machine object ( unstructured )
	unstructuredMachineObj, err := client.GetUnstructuredMachineObject(namespace, machineName)
	if err != nil {
		return fmt.Errorf("failed to get machine object: %v", err)
	}

	// At this point, we know that the host is part of some cluster since the machineRef is set.
	// There must be respctive machine object in the cluster and the machine deployment must have replicas set and greater than or equal to 1

	// TODO: Right now considering there is only one machine deployment is associated with the cluster.
	// There might be a multiple machine deployments associated with the cluster.
	// So when doing de-auth, check if the node count in the workload cluster and stop the de-auth if that is last node.

	// Check machine deployment replica count. If it is 1, then warn and ask the user to continue de-uth or not.
	replicaCount, err := client.GetMachineDeploymentReplicaCount(unstructuredMachineObj, namespace)
	if err != nil {
		return fmt.Errorf("failed to get machine deployment replica count: %v", err)
	}

	if replicaCount == 1 {
		fmt.Println("Info: Machine deployment replica count is 1. This is the last node in the cluster.")

		// Ask user to continue de-auth or not
		continueDeauth, err := utils.AskBool("Do you want to continue with de-auth? (y/n)")
		if err != nil {
			return fmt.Errorf("failed to get user input: %v", err)
		}
		if !continueDeauth {
			return fmt.Errorf("Info: De-auth cancelled by user.")
		}

		// Since this is the last machine in the cluster, annotate machine objects to exclude the node drain
		err = client.AnnotateMachineObject(unstructuredMachineObj, namespace, "machine.cluster.x-k8s.io/exclude-node-draining", "")
		if err != nil {
			return fmt.Errorf("failed to annotate the last machine object to be deauth: %v", err)
		}
	}

	// Get the fresh machine object from the server to get the updated machine object
	unstructuredMachineObj, err = client.GetUnstructuredMachineObject(namespace, machineName)
	if err != nil {
		return fmt.Errorf("failed to get machine object: %v", err)
	}

	// 5. Annonate the respective machine object with "cluster.x-k8s.io/delete-machine"="yes"
	err = client.AnnotateMachineObject(unstructuredMachineObj, namespace, "cluster.x-k8s.io/delete-machine", "yes")
	if err != nil {
		return fmt.Errorf("failed to annotate machine object: %v", err)
	}

	utils.LogSuccess("Successfully annotated machine object that needs to be removed from the cluster")

	// 6. Scale down the machine deployment by 1
	err = client.ScaleDownMachineDeployment(unstructuredMachineObj, namespace)
	if err != nil {
		return fmt.Errorf("failed to scale down machine deployment: %v", err)
	}

	utils.LogSuccess("Successfully scaled down machine deployment by 1")

	// 7. Wait for machineRef to be unset from the byohost object status field
	err = client.WaitForMachineRefToBeUnset(byoHost, namespace)
	if err != nil {
		return fmt.Errorf("failed to wait for machineRef to be unset: %v", err)
	}

	utils.LogSuccess("MachineRef successfully unset for the host")

	// If operation is decommission, delete the byohost object and run dpkg purge
	if operationType == OperationDecommission {
		return performHostDecommissionWithNoMachineRef(client, namespace)
	}

	return nil
}

// Helper function to consolidate decommissioning logic when no machineRef is set
func performHostDecommissionWithNoMachineRef(client *client.Client, namespace string) error {
	// 1. Delete the byohost object
	// 2. Run dpkg purge
	// 3. Return success

	utils.LogInfo("Deleting ByoHosts object and running dpkg purge")
	// 1. Delete the byohost object
	err := client.DeleteByoHostObject(namespace)
	if err != nil {
		return fmt.Errorf("failed to delete ByoHosts object: %v", err)
	}

	utils.LogSuccess("Successfully deleted ByoHosts object")

	// 2. Run dpkg purge
	err = service.PurgeDebianPackage()
	if err != nil {
		return fmt.Errorf("failed to run dpkg purge: %v", err)
	}

	utils.LogSuccess("Successfully ran dpkg purge")

	return nil
}
