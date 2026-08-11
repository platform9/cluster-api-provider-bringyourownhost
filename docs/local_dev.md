# How to test the Bring Your Own Host Provider locally

This doc provides instructions about how to test Bring Your Own Host Provider on a local workstation using:

- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) for provisioning a management cluster
- [Docker](https://docs.docker.com/engine/install/) run for creating hosts to be used as a capacity for BYO Host machines
- [BYOH](https://github.com/vmware-tanzu/cluster-api-provider-bringyourownhost) provider to add the above hosts to the aforemention workload cluster
- [Tilt](https://docs.tilt.dev/install.html) for faster iterative development

## Running `agent-test` / `test-e2e` on macOS

`make agent-test` and `make test-e2e` make real network/Docker calls, including spawning
containers with `--network host`. On Linux (CI's runner, or a Linux dev machine) that
genuinely shares the machine's own network namespace, so it just works. On macOS, container
runtimes (Podman machine, Docker Desktop) run everything inside a Linux VM — `--network host`
there only shares the *VM's* namespace, not the real Mac, so anything the test process itself
binds to its own loopback (envtest's API server, the management kind cluster's exposed port)
becomes unreachable from the containers the test spawns. This isn't a bug in this repo's
tests; it's inherent to how virtualization-backed container runtimes work on macOS.

Use `make agent-test-linux-vm` / `make test-e2e-linux-vm` instead: they run the whole test
process inside a Linux container sharing the podman machine's own socket, so the test process
and the containers it spawns all land in one flat Linux network namespace — the same
arrangement as real CI — with no macOS-specific workarounds needed in the test code itself.

### One-time host setup (Podman machine)

These targets assume [Podman Desktop](https://podman-desktop.io/)'s machine as the container
runtime, with a working `docker` CLI on `PATH` backed by it:

```bash
# A plain `docker` name is required: CAPI's kind-bootstrap library shells out to
# "docker -v" / "podman -v" to decide which one to use, and falls back to Docker's
# (incompatible) network flags if it can't find *either* by name on PATH.
ln -s "$(command -v podman)" /opt/homebrew/bin/docker
ln -s "$(command -v podman)" /opt/homebrew/bin/podman  # if `podman` itself isn't already on PATH
```

Give the machine enough resources to run a management cluster plus several BYO host
containers concurrently — 4+ CPUs and 8GB+ RAM has worked reliably:

```bash
podman machine stop
podman machine set --memory 8192
podman machine start
```

### Running

```bash
make agent-test-linux-vm
GINKGO_FOCUS='\[PR-Blocking\]' make test-e2e-linux-vm
SKIP_RESOURCE_CLEANUP=true GINKGO_FOCUS='\[PR-Blocking\]' make test-e2e-linux-vm
```

`GINKGO_FOCUS`, `GINKGO_SKIP`, `GINKGO_NODES`, `SKIP_RESOURCE_CLEANUP`, and
`USE_EXISTING_CLUSTER` all pass through to the containerized `make test-e2e` unchanged. The
"these tests modify system settings" confirmation prompt is answered automatically — the
changes it warns about land inside the disposable container, not on your Mac.

### Known gotchas

- **Stale cross-arch tool binaries.** `hack/tools/bin/ginkgo` and `bin/kustomize` are built
  once and reused by their Makefile targets — they don't get rebuilt just because you switched
  between running natively and running via a `*-linux-vm` target. If you see `cannot execute
  binary file` or `exec format error`, delete the stale binary (`rm hack/tools/bin/ginkgo` /
  `rm bin/kustomize`) and re-run; the next `make` invocation rebuilds it for whichever
  environment is running.
- **Killing a `*-linux-vm` run doesn't stop its container.** Interrupting the `make` command
  only kills the local `docker run` client; the container keeps running server-side. Check
  `docker ps` and `docker rm -f <name>` explicitly, or a later run can end up contending with
  a leftover one for the same ports/resources.
- **arm64 BYO hosts need a real arm64 bundle published.** `test-e2e-linux-vm` runs BYO host
  containers natively for your Mac's architecture (arm64 on Apple Silicon). The installer
  registry has arm64 entries (see `installer/registry.go`), but actually installing Kubernetes
  on the host still needs an arm64 bundle image at wherever `K8sInstallerConfig.spec.bundleRepo`
  points — if none has been published there, host provisioning will stall waiting for the
  bundle regardless of anything these targets do.

## Pre-requisites

It is required to have a docker image to be used when doing docker run for creating hosts

__Clone BYOH Repo__
```shell
git clone git@github.com:vmware-tanzu/cluster-api-provider-bringyourownhost.git
```

## Setting up the management cluster

### Creates the Kubernetes cluster

We are using [kind](https://kind.sigs.k8s.io/) to create the Kubernetes cluster that will be turned into a Cluster API management cluster later in this doc.

```shell
cat > kind-cluster.yaml <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.22.0
EOF

kind create cluster --config kind-cluster.yaml
```

### Installing Cluster API

Installing cluster API into the Kubernetes cluster will turn it into a Cluster API management cluster.

We are going using [tilt](https://tilt.dev/) in order to do so, so you can have your local environment set up for rapid iterations, as described in
[Developing Cluster API with Tilt](https://cluster-api.sigs.k8s.io/developer/core/tilt.html).

In order to do so you need to clone both https://github.com/kubernetes-sigs/cluster-api/ and https://github.com/vmware-tanzu/cluster-api-provider-bringyourownhost locally;

__Clone CAPI Repo__

```shell
git clone git@github.com:kubernetes-sigs/cluster-api.git
cd cluster-api
git checkout v1.0.0 
```

__Create a tilt-settings.json file__

Next, create a tilt-settings.json file and place it in your local copy of cluster-api:  

```shell
cat > tilt-settings.json <<EOF
{
  "default_registry": "gcr.io/k8s-staging-cluster-api",
  "enable_providers": ["byoh", "kubeadm-bootstrap", "kubeadm-control-plane"],
  "provider_repos": ["../cluster-api-provider-bringyourownhost"]
}
EOF
```

__Run Tilt__

To launch your development environment, run below command and keep it running in the shell

```shell
tilt up
```
Wait for all the resources to come up, status can be viewed in Tilt UI.
## Creating the workload cluster

Now that you have a management cluster with Cluster API and BYOHost provider installed, we can start to create a workload
cluster.

### Generating the Bootstrap Kubeconfig file
Get the APIServer and Certificate Authority Data info

```shell
APISERVER=$(kubectl config view -ojsonpath='{.clusters[0].cluster.server}')
CA_CERT=$(kubectl config view --flatten -ojsonpath='{.clusters[0].cluster.certificate-authority-data}')
```

Create a BootstrapKubeconfig CR as follows
```shell
cat <<EOF | kubectl apply -f -
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: BootstrapKubeconfig
metadata:
  name: bootstrap-kubeconfig
  namespace: default
spec:
  apiserver: "$APISERVER"
  certificate-authority-data: "$CA_CERT"
EOF
```

Once the BootstrapKubeconfig CR is created, fetch the object to copy the bootstrap kubeconfig file details from the Status field
```shell
kubectl get bootstrapkubeconfig bootstrap-kubeconfig -n default -o=jsonpath='{.status.bootstrapKubeconfigData}' > ~/bootstrap-kubeconfig.conf
```

We need one bootstrap-kubeconfig per host. Create as many bootstrap-kubeconfig files as there are number of hosts (2 for this guide)


### Add a minimum of two hosts to the capacity pool

Create Management Cluster kubeconfig
```shell
export KIND_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' kind-control-plane)
sed -i 's/    server\:.*/    server\: https\:\/\/'"$KIND_IP"'\:6443/g' ~/bootstrap-kubeconfig.conf
```
Generate host-agent binaries
```
make host-agent-binaries
```

### Create docker hosts
```shell
cd cluster-api-provider-bringyourownhost
make prepare-byoh-docker-host-image-dev
```
Run the following to create n hosts, where ```n>1```
```shell
for i in {1..n}
do
echo "Creating docker container host $i"
docker run --detach --tty --hostname host$i --name host$i --privileged --security-opt seccomp=unconfined --tmpfs /tmp --tmpfs /run --volume /var --volume /lib/modules:/lib/modules:ro --network kind byoh/node:v1.22.3
echo "Copy agent binary to host $i"
docker cp bin/byoh-hostagent-linux-amd64 host$i:/byoh-hostagent
echo "Copy kubeconfig to host $i"
docker cp ~/bootstrap-kubeconfig.conf host$i:/bootstrap-kubeconfig.conf
done
```

Start the host agent on the host and keep it running

```shell
docker exec -it $HOST_NAME bin/bash

./byoh-hostagent --bootstrap-kubeconfig bootstrap-kubeconfig.conf
```

Repeat the same steps with by changing the `HOST_NAME` env variable for all the hosts that you created.

Check if the hosts registered itself into the management cluster.

Open another shell and run
```shell
kubectl get byohosts 
```

Open a new shell and change directory to `cluster-api-provider-bringyourownhost` repository. Run below commands

```shell
export CLUSTER_NAME="test1"
export NAMESPACE="default"
export KUBERNETES_VERSION="v1.22.3"
export CONTROL_PLANE_MACHINE_COUNT=1
export WORKER_MACHINE_COUNT=1
export CONTROL_PLANE_ENDPOINT_IP=<static IP from the subnet where the containers are running>
export BUNDLE_LOOKUP_TAG=<bundle tag>
```

From ```cluster-api-provider-bringyourownhost``` folder

```shell
cat test/e2e/data/infrastructure-provider-byoh/v1beta1/templates/docker/cluster-template.yaml | envsubst | kubectl apply -f -
```

```shell
kubectl get machines 
```

Dig into host when machine gets provisioned.

```shell
kubectl get kubeadmconfig

kubectl get BYOmachines  

kubectl get BYOhost 
```

Deploy a CNI solution

```shell
kubectl get secret $CLUSTER_NAME-kubeconfig -o jsonpath='{.data.value}' | base64 -d > $CLUSTER_NAME-kubeconfig
kubectl --kubeconfig $CLUSTER_NAME-kubeconfig apply -f test/e2e/data/cni/kindnet/kindnet.yaml
```

After a short while, our nodes should be running and in Ready state.
Check the workload cluster

```shell
kubectl --kubeconfig $CLUSTER_NAME-kubeconfig get nodes
```

Or peek at the host agent logs.
## Cleanup

```shell
kubectl delete cluster $CLUSTER_NAME
docker rm -f $HOST_NAME
kind delete cluster
```

# Host Agent Installer
The installer is responsible for detecting the BYOH OS, downloading a BYOH bundle and installing/uninstalling it.

## Supported OS and Kubernetes
The current list of supported tuples of OS, kubernetes Version, BYOH Bundle Name can be retrieved with:
```shell
./cli --list-supported
```
An example output looks like:

The corresponding bundles (particular to a patch version) should be pushed to the OCI registry of choice
By default, BYOH uses projects.registry.vmware.com

Note: It may happen that a specific patch version of a k8s minor release is not available in the OCI registry

<table>
    <tr>
        <td>OS</td>
        <td>K8S Version</td>
        <td>BYOH Bundle Name</td>
    </tr>
    <tr>
        <td>Ubuntu_20.04.*_x86-64</td>
        <td>v1.24.*</td>
        <td>byoh-bundle-ubuntu_20.04.1_x86-64_k8s:v1.24.*</td>
    </tr>
    <tr>
        <td>Ubuntu_20.04.*_x86-64</td>
        <td>v1.25.*</td>
        <td>byoh-bundle-ubuntu_20.04.1_x86-64_k8s:v1.25.*</td>
    </tr>
        <tr>
        <td>Ubuntu_20.04.*_x86-64</td>
        <td>v1.26.*</td>
        <td>byoh-bundle-ubuntu_20.04.1_x86-64_k8s:v1.26.*</td>
    </tr>
</table>
The '*' in OS means that all Ubuntu 20.04 patches will be handled by this BYOH bundle.

The '*' in the K8S Version means that the k8s minor release is supported but it may happen that a byoh bundle for a specific patch may not exist n the OCI registry,

## Pre-requisites
As of writing this, the following packages must be pre-installed on the BYOH host:
- socat
- ebtables
- ethtool
- conntrack
```shell
sudo apt-get install socat ebtables ethtool conntrack
```

## Creating a BYOH Bundle
### Kubernetes Ingredients
Optional. This step describes downloading kubernetes host components for Debian.
```shell
# Build docker image
(cd installer/bundle_builder/ingredients/deb/ && docker build -t byoh-ingredients-deb .)

# Create a directory for the ingredients and download to it
(mkdir -p byoh-ingredients-download && docker run --rm -v `pwd`/byoh-ingredients-download:/ingredients byoh-ingredients-deb)
```
### Custom Ingredients
This step describes providing custom kubernetes host components. They can be copied to `byoh-ingredients-download`. Files must match the following globs:
```shell
*containerd*.tar
*kubeadm*.deb
*kubelet*.deb
*kubectl*.deb
*cri-tools*.deb
*kubernetes-cni*.deb
```

## Building a BYOH Bundle
```shell
#Build docker image
(cd installer/bundle_builder/ && docker build -t byoh-build-push-bundle .)
```

```shell
# Build a BYOH bundle and publish it to an OCI-compliant repo
docker run --rm -v `pwd`/byoh-ingredients-download:/ingredients --env BUILD_ONLY=0 byoh-build-push-bundle <REPO>/<BYOH Bundle name>
```

The specified above BYOH Bundle name must match one of the [Supported OS and kubernetes BYOH bundle names](##supported-OS-and-kubernetes)

```shell
# You can also build a tarball of the bundle without publishing. This will create a bundler.tar in the current directory and can be used for custom pushing
docker run --rm -v `pwd`/byoh-ingredients-download:/ingredients -v`pwd`:/bundle --env BUILD_ONLY=1 byoh-build-push-bundle
```

```shell
# Optionally, additional configuration can be included in the bundle by mounting a local path under /config of the container. It will be placed on top of any drop-in configuration created by the packages and tars in the bundle
docker run --rm -v `pwd`/byoh-ingredients-download:/ingredients -v`pwd`:/bundle -v`pwd`/installer/bundle_builder/config/ubuntu/20_04/k8s/1_22 --env BUILD_ONLY=1 build-push-bundle
```
