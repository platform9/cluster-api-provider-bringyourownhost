# ADR: Source k8s bundle ingredients from upstream releases instead of apt

**Status:** Blocked — do not merge; see §5 for why
**Date:** 2026-08-18
**Deciders:** TBD
**Related:** `installer/bundle_builder/` (bundle builder), `installer/internal/algo/ubuntu-templates/` (install/uninstall scripts)

---

## 1. Context

The BYOH k8s installer bundle packages kubeadm, kubelet, kubectl, containerd, crictl, and CNI
plugins into an OCI image (`installer/bundle_builder/`), pushed to
`quay.io/platform9/byoh-bundle-<os>_k8s:<k8s-version>` and pulled onto BYO hosts by
`installer/internal/algo/ubuntu-templates/install.sh.tmpl` via `imgpkg`.

Until now, `installer/bundle_builder/ingredients/deb/download.sh` sourced kubeadm/kubelet/kubectl/
cri-tools/kubernetes-cni as `.deb` packages from the Kubernetes apt repository
(`pkgs.k8s.io/core:/stable:/<major.minor>/deb/`), installed on the host via `dpkg --install`. This
has real drawbacks for BYOH's use case specifically: BYO hosts are already-provisioned Linux boxes
the operator doesn't fully control, so depending on `apt-get update` succeeding against an
external apt repo (GPG key handling, network egress, potential lock contention with other host
config management) is a heavier and more fragile runtime dependency than fetching a handful of
pinned binaries directly. It also means Ubuntu-version-specific `.deb` availability/naming quirks
leak into the bundle builder (already visible in `.ci/build-push-bundle.sh`'s per-Ubuntu-version
bundle-name switch).

kubeadm's own documented "without a package manager" install path is exactly this: download
`kubeadm`/`kubelet`/`kubectl` as raw binaries from `https://dl.k8s.io/release/<version>/bin/<os>/
<arch>/<binary>`, verify against the sibling `<binary>.sha256`, `chmod +x`, and place them on
`$PATH` — no apt/dpkg involved.

## 2. Decision

Fetch ingredients directly from their upstream release artifacts instead of apt, and install them
as plain files instead of `.deb` packages:

- **kubeadm, kubelet, kubectl** — raw binaries from `dl.k8s.io/release/<version>/bin/<os>/<arch>/`,
  each verified against its `.sha256` before use (matching Kubernetes' own documented procedure —
  the previous apt-based script did no integrity verification at all).
- **crictl** — the release tarball from `kubernetes-sigs/cri-tools`, still the actively maintained
  upstream for it.
- **CNI plugins** — the release tarball from `containernetworking/plugins`.
- **containerd** — the plain `containerd-<version>-<os>-<arch>.tar.gz` release tarball, **not** the
  `cri-containerd-cni-*` bundle the script used previously. That bundle ships its own
  `cri-containerd.DEPRECATED.txt`: it has been deprecated since containerd 1.6, "does not work on
  some Linux distributions," and will be removed in containerd 2.0. It also silently duplicates
  crictl and CNI plugins already sourced independently above, risking version skew between the two
  copies. containerd's own docs recommend fetching containerd, runc, and CNI plugins as three
  separate artifacts instead — this decision follows that guidance.
- **runc** — added as a new, separate ingredient (`opencontainers/runc` releases), since the plain
  containerd tarball — unlike the deprecated `cri-containerd-cni` bundle — does not include it.

`installer/internal/algo/ubuntu-templates/install.sh.tmpl` and `uninstall.sh.tmpl` are updated to
install/remove these files directly (`install -m 0755` for the raw binaries, `tar -x` into the
appropriate `/usr/local/...` / `/opt/cni/bin` paths for the tarballs) instead of `dpkg --install`/
`dpkg --purge`.

## 3. Consequences

**Positive.** No dependency on apt/dpkg or the Kubernetes apt repo being reachable from the host.
Ingredient versions are pinned exactly to upstream release tags rather than whatever apt happens
to resolve. kubeadm/kubelet/kubectl integrity is now checksum-verified before installation. Removes
a deprecated upstream artifact (`cri-containerd-cni-*`) before its containerd-2.0 removal breaks
the bundle outright, and removes the crictl/CNI-plugins duplication that artifact caused.

**Negative / cost.** The bundle builder now makes six separate upstream HTTP round-trips per build
instead of one `apt-get download` batch; acceptable since bundle builds are infrequent and manual
(see §5's related finding). `RUNC_VERSION` is a new version knob that must be bumped independently
of `CONTAINERD_VERSION` going forward — previously the deprecated bundle included a runc that
tracked containerd's own release cadence for free.

## 4. Alternatives considered

| Alternative | Why rejected |
|---|---|
| **Keep the `cri-containerd-cni-*` bundle, accept the deprecation** | Works today, smaller diff, but ships an artifact upstream has explicitly flagged for removal in containerd 2.0 — this bundle would need to be revisited again regardless, just later and under time pressure once it actually breaks. |
| **Switch only kubeadm/kubelet/kubectl to raw binaries, leave crictl/CNI/containerd on apt** | Rejected: apt's `cri-tools`/`kubernetes-cni` packages are the same fragile external-repo dependency this ADR is trying to remove; a half-migration keeps most of the original problem. |
| **Vendor/mirror upstream artifacts into an internal registry before the bundle builder consumes them** | Would remove the runtime dependency on GitHub/dl.k8s.io availability during bundle builds, but is a larger infrastructure change (mirroring, retention, update automation) out of scope for this fix; nothing here precludes adding it later. |

## 5. Why this is blocked

**The install/uninstall script changes in §2 are not safely deployable on their own, because the
bundle OCI tag and the manager binary that renders these scripts are not version-linked.**

`installer/bundle_downloader.go`'s `GetBundleAddr` constructs the pull tag as
`<repo>/<bundle-name>:<k8s-version>` — e.g. `quay.io/platform9/byoh-bundle-ubuntu_22.04_x86-64_k8s:
v1.32.2`. That tag encodes only the Kubernetes version, nothing about the *bundle's internal file
layout*. `install.sh.tmpl`/`uninstall.sh.tmpl` are embedded in the **manager** binary
(`//go:embed`, `installer/internal/algo/common_ubuntu.go`) and rendered fresh on every
`K8sInstallerConfig` reconcile — a manager rollout and a bundle push to quay.io happen on
completely independent schedules (one via controller deployment, the other via a human manually
running `.ci/build-push-bundle.sh`).

Concretely, both directions break:

- **A manager running this ADR's new templates, pulling an existing `v1.32.2` tag that still has
  the old `.deb`-based content** (because nobody has re-pushed it yet) — `install -m 0755
  "$BUNDLE_PATH/kubeadm"` fails outright, no `kubeadm` file exists in that bundle.
- **A manager still running the old `.deb`-based templates, after someone pushes a `v1.32.2` tag
  rebuilt with this ADR's new content** — `dpkg --install "$BUNDLE_PATH/kubeadm.deb"` fails
  outright, no `.deb` exists in the new bundle.

Since `v1.32.2` is a single mutable tag reused by every manager version that has ever requested
that Kubernetes version, there is no window in which an old-format and new-format bundle can both
be served correctly from that tag — re-pushing it to fix one manager's install path breaks every
other manager (past or future) still relying on the old format at that same tag. **Any already-
deployed manager in production is at risk the moment this bundle format changes and the
corresponding tag gets re-pushed**, regardless of merge order between the manager change and the
bundle push.

This is not a testing gap — it is a missing versioning axis in the bundle addressing scheme
itself. Resolving it requires the OCI tag to also encode a bundle-format/schema version (so old
and new formats live at non-colliding tags), which touches `installer/bundle_downloader.go`,
`installer/registry.go`, and `controllers/infrastructure/k8sinstallerconfig_controller.go` — a
separate, larger change than this ADR's ingredient-sourcing fix. Until that's designed and landed,
merging §2's install/uninstall script changes ahead of it (or behind it, in isolation) can break
already-deployed managers, and this PR must stay in draft / not be merged.
