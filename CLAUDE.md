# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Project Does

Cluster API Provider BYOH (BringYourOwnHost) is a Kubernetes infrastructure provider that lets operators declare, provision, and manage Kubernetes clusters on already-provisioned Linux hosts. It decouples node provisioning from host provisioning by running an agent daemon on each BYO host that registers with a management cluster.

This repo (`platform9/cluster-api-provider-bringyourownhost`) is Platform9's fork of the upstream `vmware-tanzu/cluster-api-provider-bringyourownhost`. The root Go module keeps the upstream import path (`github.com/vmware-tanzu/cluster-api-provider-bringyourownhost`) for compatibility with existing imports; new Platform9-authored code (`cmd/byohctl`) uses its own module under the `github.com/platform9/...` path instead. Keep this distinction in mind when adding imports — don't assume the whole tree shares one module or one import prefix.

## Build & Development Commands

```bash
# Build
make build                    # Build manager binary to bin/manager
make host-agent-binaries      # Build host agent binaries
cd cmd/byohctl && make build  # Build byohctl CLI (separate Go module, see Architecture)

# Run tests
make test                     # All unit tests with coverage (includes cmd-test)
make controller-test          # Controller tests only
make agent-test               # Agent tests only
make webhook-test             # Webhook tests only
make cmd-test                 # byohctl tests (cd cmd && go test ./...)
make test-e2e                 # End-to-end tests (requires a cluster)
make test-e2e-linux-vm        # E2E tests in Linux VM (use on macOS to avoid false negatives)

# Code quality
make lint                     # Run golangci-lint
make fmt                      # go fmt
make vet                      # go vet

# Code generation (run after changing types or markers)
make generate                 # Regenerate DeepCopy methods
make manifests                # Regenerate CRDs and RBAC from markers

# Deploy
make install                  # Install CRDs into cluster ($KUBECONFIG)
make deploy                   # Deploy controller to cluster
make run                      # Run controller locally against $KUBECONFIG cluster
```

### Prefer `make` targets over raw `go`/`gofmt`

Default to `make <target>` (`build`, `vet`, `fmt`, `lint`, `*-test`) instead of typing
`go build`/`go test`/`go vet`/`go run`/`gofmt` directly — even when the flags would be identical
to what the target runs internally. Targets pin this repo's `GOTOOLCHAIN` (`Makefile:8`), set up
required env/tooling (`fetch_ext_bins.sh`), pass build tags a raw invocation easily misses (e.g.
`-tags=e2e` for `test/e2e`), and scope `golangci-lint`'s cache per-worktree to avoid cross-worktree
cache poisoning when `make lint` runs concurrently in other worktrees. Skipping the target tends to
produce false-negative failures (or false-positive lint findings against the wrong files) that
look like real regressions rather than faster feedback.

```bash
# Full suites
make controller-test   # ./controllers/infrastructure/
make agent-test        # ./agent/...
make webhook-test      # ./apis/infrastructure/v1beta1
make test-e2e-linux-vm GINKGO_FOCUS="test description"   # e2e, scoped to one spec (macOS-safe)
```

**Known gap:** `controller-test`/`agent-test`/`webhook-test` don't expose a focus/skip variable the
way `test-e2e` does via `GINKGO_FOCUS`. If you genuinely need to scope one of those down to a
single spec while iterating, it's fine to drop to `ginkgo -focus "description" <pkg>` or
`go test -run "TestName" <pkg>` for that one iteration — but re-run the full `make <target>`
before treating the change as verified. The raw command is a speed shortcut for iteration, not a
substitute for the make-driven signal.

On macOS, always use the `-linux-vm` variant for anything that spins up docker/systemd containers
(`agent-test-linux-vm`, `test-e2e-linux-vm`) — plain targets give false-negative failures in the
macOS sandbox (no real dockerd, no `/etc/os-release`), not real regressions.

## Architecture

### Three-Binary Design

The project produces three binaries, two of which share the root Go module:
- **Manager** (`main.go`) — runs in the management cluster; reconciles `ByoCluster`, `ByoMachine`, `ByoHost`, and related CRs.
- **Host Agent** (`agent/main.go`) — runs as a daemon on each BYO host; registers the host with the management cluster and drives Kubernetes installation.
- **byohctl** (`cmd/byohctl/`) — operator-facing CLI for onboarding, deauthorizing, and decommissioning a host (`cmd/byohctl/cmd/{onboard,deauthorise,decommission}.go`). Lives in its own Go module (`cmd/go.mod`) with its own `cmd/byohctl/Makefile`; built and tested independently of the root module — see `make cmd-test`.

### Custom Resources (`apis/infrastructure/v1beta1/`)

| Resource | Purpose |
|---|---|
| `ByoHost` | Represents a registered Linux host; the agent creates/owns this |
| `ByoMachine` | Represents a Kubernetes node backed by a BYO host |
| `ByoCluster` | Represents the cluster-level infrastructure |
| `K8sInstallerConfig` | Installer-specific configuration for a host |
| `BootstrapKubeconfig` | Manages the kubeconfig secret used by the agent to bootstrap |
| `ByoMachineTemplate` / `ByoClusterTemplate` | Reusable templates for the above |

All types follow the Cluster API condition contract and use finalizers for cleanup.

### Controllers (`controllers/infrastructure/`)

Each controller file maps 1:1 to a resource. Key reconciliation flows:

- `byomachine_controller.go` — the most complex; matches a `ByoMachine` to an available `ByoHost`, injects bootstrap data, and tracks node registration status. Uses `byomachine_scope.go` as a context helper. The only controller that uses the `ClusterCacheTracker` (wired in `main.go` as `Tracker: tracker`) to watch workload-cluster objects from the management cluster; all other controllers use only the local management cluster client.
- `byohost_controller.go` — manages host lifecycle; cleans up when hosts are released.
- `byoadmission_controller.go` — auto-approves CSRs from registered hosts.
- `bootstrapkubeconfig_controller.go` — creates the short-lived kubeconfig the agent uses to register itself.
- `k8sinstallerconfig_controller.go` — resolves OCI bundle references and renders installer scripts onto `ByoHost` objects; drives the k8s install/uninstall lifecycle from the management-cluster side.
- `byomachinetemplate_controller.go` — validates immutability of `ByoMachineTemplate` fields.

### Host Agent (`agent/`)

Sub-packages:
- `registration/` — registers the host as a `ByoHost` CR using the bootstrap kubeconfig
- `reconciler/` — reconciliation loop that drives k8s installation/removal based on `ByoHost` spec
- `cloudinit/` — generates and executes cloud-init scripts for bootstrap data

### Installer Framework (`installer/`)

Pluggable installer interface. Kubernetes components are distributed as OCI bundle images. `bundle_builder/` creates these images; `bundle_downloader.go` fetches them at install time; `registry.go` resolves bundle references.

## Testing Patterns

Tests use **Ginkgo v2** + **Gomega** and **Counterfeiter** for mocks (generated fakes live in `**/fakes/` directories). Controller tests use `envtest` (real API server + etcd) rather than mocking the client. E2E tests live in `test/e2e/` and require a real cluster.

When adding a new controller or type, run `make generate && make manifests` before writing tests.

A throwaway local registry is available if future e2e work needs to serve a real built artifact without touching quay.io: `test/e2e/e2e_agent_bundle_registry.go`'s `ensureLocalAgentBundleRegistry` builds and serves an agent `.deb` bundle this way for the `byohctl` e2e spec (`test/e2e/e2e_byohctl_test.go`), since quay.io's published bundle is only tagged for commits already on `main`/`ci-*`. Be aware it only kicks in if `build/pf9-byohost/debsrc/` already exists (from `make build-host-agent-deb`, run separately in `.github/workflows/e2e.yml` — not part of `make test-e2e`'s own dependency chain, since it needs `fpm`/ruby installed); without that prerequisite, the spec skips locally but **fails** (not skips) when `CI` is set, so a broken prerequisite can't silently drop coverage.

## Code Generation

After editing type files in `apis/`, always regenerate:
```bash
make generate   # updates zz_generated.deepcopy.go
make manifests  # updates config/crd/bases/ and config/rbac/
```

Markers in type files (e.g. `// +kubebuilder:object:root=true`) drive controller-gen; don't edit generated files by hand.

## Linting

Config is in `.golangci.yml` (v2 schema, timeout 10 min). Key enabled linters include `gosec`, `staticcheck`, `errcheck`, `gocyclo`, and `depguard`. Run `make lint` before submitting; CI enforces this via `golangci-lint-action@v9` pinned to v2.12.2 (`.github/workflows/lint.yml`).

Gotcha: the `golangci-lint` target in the Makefile only installs the binary if `bin/golangci-lint` doesn't already exist, and pins an older v1.64.8 install script — if you have a stale v1 binary in `bin/`, `make lint` will run against the v2-schema config and fail or disagree with CI. Delete `bin/golangci-lint` and re-run `make lint` if results look wrong.

## Licensing

Most existing files carry a VMware copyright header, e.g.:
```go
// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
```

- Never remove or replace an existing copyright header, including VMware's.
- When editing a file going forward, add a Platform9 copyright line above the `SPDX-License-Identifier` line (don't replace the existing one) using the current year:
```go
// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
```
- New files that have no prior header get a Platform9-only header.
