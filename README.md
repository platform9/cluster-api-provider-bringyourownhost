# Kubernetes Cluster API Provider Bring Your Own Host (BYOH)
<p align="center">
<!-- lint card --><a href="https://github.com/platform9/cluster-api-provider-bringyourownhost/actions/workflows/lint.yml">
<img src="https://github.com/platform9/cluster-api-provider-bringyourownhost/actions/workflows/lint.yml/badge.svg"></a>
<!-- test status -->
<a href="https://github.com/platform9/cluster-api-provider-bringyourownhost/actions?query=event%3Apush+branch%3Amain+workflow%3ACI+">
<img src="https://github.com/platform9/cluster-api-provider-bringyourownhost/actions/workflows/ci.yml/badge.svg?branch=main&event=push"></a>
<!-- go doc / reference card -->
<a href="https://pkg.go.dev/github.com/vmware-tanzu/cluster-api-provider-bringyourownhost">
<img src="https://pkg.go.dev/badge/github.com/vmware-tanzu/cluster-api-provider-bringyourownhost.svg"></a>
<!-- codecov badge -->
<a href="https://codecov.io/gh/platform9/cluster-api-provider-bringyourownhost">
<img src="https://codecov.io/gh/platform9/cluster-api-provider-bringyourownhost/branch/main/graph/badge.svg"></a>
</p>

------

## What is Cluster API Provider BYOH

[Cluster API](https://github.com/kubernetes-sigs/cluster-api) brings
declarative, Kubernetes-style APIs to cluster creation, configuration and
management.

__BYOH__ is a Cluster API Infrastructure Provider for already-provisioned hosts running Linux. This provider allows operators to adopt Cluster API for deploying and managing kubernetes nodes without also having to adopt a specific infrastructure service. This enables users to decouple kubernetes node provisioning from host and infrastructure provisioning.

## BYOH Glossary
**Host** - A host is a running computer system. It could be physical or virtual. It has a kernel and some base operating system

**BYO Host** - A Linux host provisioned and managed outside of Cluster API

**BYOH Capacity Pool** - A set of BYO Hosts registered in a management cluster & authorized for usage as a capacity for deploying Kubernetes nodes

**Kubernetes Node** - A Kubernetes Node that runs on top of a Host. There is a 1-to-1 relationship between nodes and hosts (every host has zero or one nodes). Node provisioning and lifecycle management is a Cluster API responsibility

**Kubernetes Host Components** - The components that run uncontainerized on the host and are required to bootstrap a Kubernetes node. Typically, this is at least kubelet, containerd and kubeadm, but different OS might require different components in this category

## Features

- Native Kubernetes manifests and API
- Support for single and multi-node control plane clusters
- Support already provisioned Linux VMs with Ubuntu 22.04 and 24.04

## Getting Started
Check out the [getting_started](https://github.com/platform9/cluster-api-provider-bringyourownhost/blob/main/docs/getting_started.md) guide for launching a BYOH workload cluster

For an IPv6 or dual-stack control-plane endpoint, see [IPv6 and dual-stack support](./docs/ipv6_support.md).

## Community, discussion, contribution, and support

BYOH was originally created by the [VMware Tanzu](https://github.com/vmware-tanzu/cluster-api-provider-bringyourownhost) team as a Cluster API sub-project. It has since been adopted by [Platform9](https://platform9.com), who now maintains this fork.

The best way to ask questions, report bugs, or discuss the project is to open an issue on the [issue tracker](https://github.com/platform9/cluster-api-provider-bringyourownhost/issues).

Pull Requests and feedback on issues are very welcome!
See the [issue tracker](https://github.com/platform9/cluster-api-provider-bringyourownhost/issues) if you're unsure where to start, especially the [Good first issue](https://github.com/platform9/cluster-api-provider-bringyourownhost/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22) and [Help wanted](https://github.com/platform9/cluster-api-provider-bringyourownhost/issues?q=is%3Aopen+is%3Aissue+label%3A%22help+wanted%22) tags, and
also feel free to reach out to discuss.

See also our [contributor guide](CONTRIBUTING.md) and the Kubernetes [community page](https://kubernetes.io/community) for more details on how to get involved.


## Project Status

This project is currently a work-in-progress, in an Alpha state, so it may not be production ready. There is no backwards-compatibility guarantee at this point. For more details on the roadmap and upcoming features, check out the project's [issue](https://github.com/platform9/cluster-api-provider-bringyourownhost/issues) tracker on GitHub.


## Getting involved and contributing

### Go version management

This project uses [asdf](https://asdf-vm.com) to pin the Go version. The required version is declared in `.tool-versions`.

Install asdf by following the [official instructions](https://asdf-vm.com/guide/getting-started.html) for your platform. 

Install the Go plugin and the pinned version:

```bash
asdf plugin add golang
asdf install
```

`asdf install` reads `.tool-versions` and installs the exact version. After that, `go version` in this directory will report the pinned version.

To verify, for example:

```bash
$ go version
go version go1.26.4 darwin/arm64
```
### Launching a Kubernetes cluster using BYOH source code

Check out the [developer guide](./docs/local_dev.md) for launching a BYOH cluster consisting of Docker containers as hosts.

More about development and contributing practices can be found in [`CONTRIBUTING.md`](./CONTRIBUTING.md).

## Implement Custom Installer controller
An installer controller is responsible to provide the installation and uninstallation scripts for k8s dependencies, prerequisites and components on each `BYOHost`.  
If someone wants to implement their own installer controller then they need to follow the contract defined in [installer](./docs/installer.md) doc.

------

## Compatibility with Cluster API

- BYOH is currently compatible with Cluster API v1beta1, built/tested against Cluster API v1.10.10

## Supported OS and Kubernetes versions
| Operating System  | Architecture  | Kubernetes v1.31 - v1.35 |
| ------------------|---------------|:------------------------:|
| Ubuntu 22.04.*    | amd64         |            ✓             |
| Ubuntu 24.04.*    | amd64         |            ✓             |

**NOTE:**  The '*' in OS means that all patches of that Ubuntu release are supported.

**NOTE:**  The Kubernetes range means that minor release is supported but it may happen that a BYOH bundle for a specific patch may not exist in the OCI registry. 

## BYOH in News
- [TGIK episode on BYOH](https://www.youtube.com/watch?v=Xwm5Ka27-Io&t=2838s)
- BYOH presented during [Cluster API Office Hours](https://www.youtube.com/watch?v=6ODMLgX-dz4&t=572s)
- [BYOH on ARM](https://williamlam.com/2021/11/hybrid-x86-and-arm-kubernetes-clusters-using-tanzu-community-edition-tce-and-esxi-arm.html)

