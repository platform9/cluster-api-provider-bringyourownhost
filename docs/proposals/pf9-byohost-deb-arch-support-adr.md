# ADR: Make `pf9-byohost-agent` deb/rpm packaging architecture-aware

**Status:** Implemented — `make test-packaging-linux-vm` passes on both amd64 and arm64 build hosts
**Date:** 2026-08-14
**Deciders:** TBD
**Related:** [`pf9-byohost.spec` cleanup ADR](pf9-byohost-spec-cleanup-adr.md) (already merged to `main`)

---

## 1. Context

**`Architecture: all` on a package containing an amd64-only binary.**
The `fpm` invocation passed `--architecture all`, but the payload — staged from
`build-host-agent-binary`, which hardcodes `GOARCH=amd64` — is a compiled amd64 ELF binary.
Debian policy draws a hard line here: `all` means "no architecture-specific content,"
specifically so `apt`/`dpkg` on any other architecture will accept and "successfully" install
it. On an arm64 host this package installed cleanly and enabled a systemd unit whose
`ExecStart` could never execute — a failure mode `apt` couldn't warn about because the
package's own metadata claimed it was portable.

This surfaced concretely once these fixes were run against `make test-packaging-linux-vm` on an
actual arm64 build host (this repo's e2e-linux-vm tooling is used specifically so Apple Silicon
Macs can exercise Linux-only packaging tooling — `fpm`, `rpmbuild`, `dpkg`, `rpm` — that doesn't
exist on macOS): fixing `Architecture: all` → `amd64` made `dpkg -i` *correctly* refuse the
package inside the test's `byoh/node:e2e` container, because that container is built natively on
whatever host is running the test — arm64 on an Apple Silicon Mac's Podman machine. The RPM suite
"passed" for a different, worse reason: `rpmbuild` infers its package `Architecture` implicitly
from the build host (so it correctly said `aarch64` on this machine), while the binary staged
inside was — like the deb's — always the hardcoded amd64 build. That's a *more* dangerous version
of the same bug: a package that claims to be `aarch64` while literally embedding an amd64 binary,
invisible to `rpm` because it never inspects the binary's real machine type against the package's
declared arch. Both formats trace to the same root cause: `$(COMMON_SRC_ROOT)`, shared by both,
always staged the binary from `build-host-agent-binary` (the published-release target, hardcoded
`GOARCH=amd64`) regardless of what host was actually building the package.

---

## 2. Decision

**Make packaging build for whatever host is actually invoking it, for both formats, and stop
embedding the architecture in the installed binary's filename.** A new `PACKAGE_GOARCH`
Makefile variable defaults to `amd64`/`arm64`, overridable (e.g.
`PACKAGE_GOARCH=amd64 make build-host-agent-deb` to cross-build) — the same principle
`rpmbuild` already applies implicitly via its own build-host arch detection, just made
explicit and shared by both formats. Reuses `HELM_ARCH`'s existing `uname -m` ->
`amd64`/`arm64` mapping rather than adding a second one — this Makefile already had three
independent `uname -m` call sites (the Helm download logic, and two in
`PF9_BYOHOST_RPM_FILE`'s own path); `PACKAGE_GOARCH` reuses `HELM_ARCH` directly and
`PF9_BYOHOST_RPM_FILE` was switched to `HELM_ARCH_RAW` in the same pass, leaving one arch
detection instead of what would otherwise have grown to four. `$(COMMON_SRC_ROOT)` now builds the
host-agent binary for `$(PACKAGE_GOARCH)` directly (independent of
`build-host-agent-binary`/`$(RELEASE_DIR)`, which stay pinned to amd64 for the published release
artifact — packaging and the release pipeline no longer share a binary-build step), and stages it
as `/binary/pf9-byoh-hostagent` — no `-linux-amd64` suffix. The suffix was redundant the moment
the package itself carries an `Architecture` field: `dpkg`/`rpm` already guarantee only the
matching arch's package installs, so a single arch-neutral path is one less place
(`after-install.sh`, `before-remove.sh`, the RPM spec's `%install`/`%files`,
`service/pf9-byohostagent.service`'s `ConditionPathExists`/`ExecStart`) that has to independently
agree on what the binary is called. `fpm --architecture` now reads `$(PACKAGE_GOARCH)` directly —
Debian's arch names happen to equal Go's `GOARCH` values for `amd64`/`arm64`, so no separate
mapping table is needed there.

---

## 3. Consequences

**Positive.** `apt`/`dpkg` and `rpm` on a mismatched host now correctly refuse this package
instead of installing a binary that can't run — on *either* format, not just deb. `make
test-packaging-linux-vm` — the only thing that actually exercises this code path — now passes on
an arm64 build host instead of silently mis-packaging or hard-failing on the one case it's most
useful for: contributors on Apple Silicon.

**Negative / cost.** Packaging and the release pipeline no longer share `build-host-agent-binary`
— an extra host-agent binary gets built when running `make build-host-agent-deb`/`-rpm` standalone
outside of testing, where before it could reuse a release build already sitting in
`$(RELEASE_DIR)`. Accepted: the two were never guaranteed to agree on architecture even before
this change (nothing pinned `$(RELEASE_DIR)`'s contents to what `build-host-agent-deb` needed at
the time it ran, they just happened to always both be amd64), so the sharing was incidental, not
load-bearing. Anyone invoking `build-host-agent-deb`/`-rpm` directly on a non-amd64 machine and
expecting an amd64 artifact now needs `PACKAGE_GOARCH=amd64` explicitly — previously that was the
only possible output, now it's the default only on an amd64 host.

**Risk if not done.** Architecture-mismatched installs keep "succeeding" and failing silently at
service-start time instead of at install time, on both formats. `make test-packaging-linux-vm`
stays broken on arm64 build hosts, which was the explicit goal this round of cleanup was done to
unblock.

---

## 4. Alternatives considered

| Alternative | Why rejected |
|---|---|
| **Keep `--architecture` a literal (`amd64`) instead of deriving it from the build host** | This was the original plan — rejected once `make test-packaging-linux-vm` on an arm64 Mac showed the literal doesn't hold for the one host class this tooling exists to support. A derived value costs nothing on amd64 (still defaults to `amd64` there) and fixes the arm64 case for free. |
| **Use QEMU/binfmt emulation in the test container instead of building packages for the host's real architecture** | Would let the amd64-only pipeline stay as-is and paper over the mismatch at test time instead of fixing it. Rejected because the underlying bug (package metadata not matching package contents) is real independent of how it's tested, and because emulation for the *entire* test container is a heavier, slower dependency than making the Makefile build for the arch it's already running on. |
| **Keep the `-linux-$(GOARCH)` suffix on the installed binary path, and thread `$(PACKAGE_GOARCH)` through the hook scripts and RPM spec to reference it dynamically** | Would work, but means every one of `after-install.sh`, `before-remove.sh`, the RPM spec, and the systemd unit needs its own way to learn the current package's architecture at install/uninstall time (env var, generated file, `uname`-sniffing) just to reconstruct a filename the package's own `Architecture` field already makes redundant. Dropping the suffix removes the need for that machinery entirely — one arch-neutral literal path, everywhere. |
