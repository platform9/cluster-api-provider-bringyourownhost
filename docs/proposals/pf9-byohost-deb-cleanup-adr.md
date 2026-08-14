# ADR: Clean up the `pf9-byohost-agent` deb packaging

**Status:** Implemented — all §2 decision items done, `make test-packaging-linux-vm` passes on both amd64 and arm64 build hosts
**Date:** 2026-08-14
**Deciders:** TBD
**Related:** [`pf9-byohost.spec` cleanup ADR](pf9-byohost-spec-cleanup-adr.md) (already merged to `main`)

---

## 1. Context

The RPM cleanup ADR treated the deb `fpm` hook scripts as the *authoritative* side when
converging `%post`/`%preun`/`%postun` onto them (its §2 item 5) — reasonable, since they had more
complete cleanup logic than the RPM spec did at the time. But "more complete than the RPM spec"
is not the same as "correct," and nothing in that work actually audited the deb scripts, the RPM
spec's binary path, or the `fpm` invocation on their own merits. `build-host-agent-deb` was not
wired into any CI workflow until the RPM ADR's `packaging` job landed, same gap that ADR found for
`build-host-agent-rpm`.

**Findings, in the order they surfaced:**

- **`Architecture: all` on a package containing an amd64-only binary.**
  The `fpm` invocation passed `--architecture all`, but the payload — staged from
  `build-host-agent-binary`, which hardcodes `GOARCH=amd64` — is a compiled amd64 ELF binary.
  Debian policy draws a hard line here: `all` means "no architecture-specific content,"
  specifically so `apt`/`dpkg` on any other architecture will accept and "successfully" install
  it. On an arm64 host this package installed cleanly and enabled a systemd unit whose
  `ExecStart` could never execute — a failure mode `apt` couldn't warn about because the
  package's own metadata claimed it was portable.

- **`before-remove.sh`'s config-directory cleanup was a silent no-op.**
  ```sh
  if [ -f /etc/pf9-byohost* ]; then
      rm -f /etc/pf9-byohost*
  fi
  ```
  The only thing on the host actually matching `/etc/pf9-byohost*` is the *directory*
  `/etc/pf9-byohost-agent.service.d/` (created by `after-install.sh`, holding the
  `pf9-byohost-agent.conf` the RPM ADR's whole §2 items 4–5 were about getting written in the
  first place). `[ -f <directory> ]` is false — `-f` only matches regular files — so this branch
  never executed, on every uninstall, unconditionally. The directory and its conf file were left
  behind permanently. `deb_install_test.go` didn't catch this because it only asserted the binary
  and unit file were gone after `dpkg -r` — it never asserted the conf directory was gone too.

- **`after-remove.sh` had no shebang.** The entire file was one `echo` line with no `#!/bin/bash`
  (or anything else), unlike its two siblings (`after-install.sh`, `before-remove.sh`, both
  `#!/bin/bash`). It happened to run today only because glibc's `execvp`/`execlp` retries under
  `/bin/sh` on `ENOEXEC` when a file lacks recognizable exec-format magic bytes — an
  implementation-defined compatibility shim, not a documented `dpkg` guarantee, and inconsistent
  with how the other two hooks are written.

- **Uninstall log path didn't match every other pf9 log path.** `before-remove.sh` wrote to
  `/var/log/pf9/byoh-agent-uninstall.log`. Every other log this package writes —
  `after-install.sh`'s `byoh-agent.log`, and the file `before-remove.sh` itself checks for and
  removes — lives one level down, under `/var/log/pf9/byoh/`. (The RPM ADR flagged the
  *symmetric* version of this — the old `%preun` had the `byoh/` segment `before-remove.sh` was
  missing — as a sign the two had drifted; once `%preun` converged onto this file, this file's own
  path turned out to be the one that was actually wrong.)

**A fifth problem surfaced only once §2's fixes were run against `make test-packaging-linux-vm`
on an actual arm64 build host** (this repo's e2e-linux-vm tooling is used specifically so
Apple Silicon Macs can exercise Linux-only packaging tooling — `fpm`, `rpmbuild`, `dpkg`, `rpm` —
that doesn't exist on macOS): fixing `Architecture: all` → `amd64` made `dpkg -i` *correctly*
refuse the package inside the test's `byoh/node:e2e` container, because that container is built
natively on whatever host is running the test — arm64 on an Apple Silicon Mac's Podman machine.
The RPM suite's Rocky container "passed" for a different, worse reason: `rpmbuild` infers its
package `Architecture` implicitly from the build host (so it correctly said `aarch64` on this
machine), while the binary staged inside was — like the deb's — always the hardcoded amd64 build.
That's a *more* dangerous version of the same bug: a package that claims to be `aarch64` while
literally embedding an amd64 binary, invisible to `rpm` because it never inspects the binary's
real machine type against the package's declared arch. Both formats trace to the same root cause:
`$(COMMON_SRC_ROOT)`, shared by both, always staged the binary from `build-host-agent-binary` (the
published-release target, hardcoded `GOARCH=amd64`) regardless of what host was actually building
the package.

---

## 2. Decision

1. **Fix `--architecture all` → match the architecture of the binary actually staged.** See item
   5 — this became a derived value (`PACKAGE_GOARCH`) rather than a literal, once item 5's
   testing exposed that a literal `amd64` doesn't hold on an arm64 build host either.

2. **Fix the conf-directory cleanup to actually match and recurse**
   (`scripts/pf9-byohost-agent-before-remove.sh`): `[ -f /etc/pf9-byohost* ]` → `[ -e
   /etc/pf9-byohost-agent.service.d ]`, `rm -f` → `rm -rf`. Name the exact path instead of a glob:
   the glob added nothing (only ever matched the one directory) and a literal path can't
   silently stop matching if something else under `/etc/` starts with the same prefix later.

3. **Add `#!/bin/bash` to `after-remove.sh`.** Matches its two siblings; removes the dependence
   on the `execvp` `ENOEXEC` fallback.

4. **Fix the uninstall log path**: `before-remove.sh`'s `LOG_FILE` →
   `/var/log/pf9/byoh/byoh-agent-uninstall.log`, matching every other log path in this file and in
   `after-install.sh`. `mkdir -p -m 0755` the directory first rather than assuming
   `after-install.sh` already created it — `before-remove.sh` must not assume install-time
   ordering it doesn't control (e.g. a partially-failed install) — with an explicit mode rather
   than the ambient umask, since a shared log directory's permissions shouldn't vary with whatever
   umask the caller happens to be running under.

5. **Make packaging build for whatever host is actually invoking it, for both formats, and stop
   embedding the architecture in the installed binary's filename.** A new `PACKAGE_GOARCH`
   Makefile variable defaults to `amd64`/`arm64`, overridable (e.g.
   `PACKAGE_GOARCH=amd64 make build-host-agent-deb` to cross-build) — the same principle
   `rpmbuild` already applies implicitly via its own build-host arch detection, just made
   explicit and shared by both formats. Reuses `HELM_ARCH`'s existing `uname -m` ->
   `amd64`/`arm64` mapping rather than adding a second one — this Makefile already had three
   independent `uname -m` call sites (the Helm download logic, and two in
   `PF9_BYOHOST_RPM_FILE`'s own path); `PACKAGE_GOARCH` reuses `HELM_ARCH` directly and
   `PF9_BYOHOST_RPM_FILE` was switched to `HELM_ARCH_RAW` in the same pass, leaving one arch
   detection instead of what would otherwise have grown to four. `$(COMMON_SRC_ROOT)` now builds the host-agent binary for
   `$(PACKAGE_GOARCH)` directly (independent of `build-host-agent-binary`/`$(RELEASE_DIR)`, which
   stay pinned to amd64 for the published release artifact — packaging and the release pipeline
   no longer share a binary-build step), and stages it as `/binary/pf9-byoh-hostagent` — no
   `-linux-amd64` suffix. The suffix was redundant the moment the package itself carries an
   `Architecture` field: `dpkg`/`rpm` already guarantee only the matching arch's package installs,
   so a single arch-neutral path is one less place (`after-install.sh`, `before-remove.sh`, the
   RPM spec's `%install`/`%files`, `service/pf9-byohostagent.service`'s `ConditionPathExists`/
   `ExecStart`) that has to independently agree on what the binary is called. `fpm --architecture`
   now reads `$(PACKAGE_GOARCH)` directly — Debian's arch names happen to equal Go's `GOARCH`
   values for `amd64`/`arm64`, so no separate mapping table is needed there.

---

## 3. Consequences

**Positive.** `apt`/`dpkg` and `rpm` on a mismatched host now correctly refuse this package
instead of installing a binary that can't run — on *either* format, not just deb. Uninstall
actually removes everything it claims to in its own log output — including the file the RPM
ADR's whole conf-generation fix was about creating. `after-remove.sh` stops depending on an
implementation-defined exec fallback. All uninstall logging lands in one directory, matching
install-time logging. `make test-packaging-linux-vm` — the only thing that actually exercises this
code path — now passes on an arm64 build host instead of silently mis-packaging or (post-item-1,
pre-item-5) hard-failing on the one case it's most useful for: contributors on Apple Silicon.

**Negative / cost.** Packaging and the release pipeline no longer share `build-host-agent-binary`
— an extra host-agent binary gets built when running `make build-host-agent-deb`/`-rpm` standalone
outside of testing, where before it could reuse a release build already sitting in
`$(RELEASE_DIR)`. Accepted: the two were never guaranteed to agree on architecture even before this
change (nothing pinned `$(RELEASE_DIR)`'s contents to what `build-host-agent-deb` needed at the
time it ran, they just happened to always both be amd64), so the sharing was incidental, not load-bearing.
Item 2 also means re-installing after an uninstall now starts from a clean
`/etc/pf9-byohost-agent.service.d/`, rather than one that silently still has the previous
install's `NAMESPACE`/`REGION` values in it until `after-install.sh` overwrites them (which it
does unconditionally, so this is very unlikely to be relied upon — but worth a release-note line
alongside the RPM ADR's own behavior-change callout, since both stem from the same conf file).
Anyone invoking `build-host-agent-deb`/`-rpm` directly on a non-amd64 machine and expecting an
amd64 artifact now needs `PACKAGE_GOARCH=amd64` explicitly — previously that was the only
possible output, now it's the default only on an amd64 host.

**Risk if not done.** Architecture-mismatched installs keep "succeeding" and failing silently at
service-start time instead of at install time, on both formats. Uninstalls keep leaving
`/etc/pf9-byohost-agent.service.d/` behind indefinitely. `after-remove.sh` keeps working by
accident rather than by contract. `make test-packaging-linux-vm` stays broken on arm64 build
hosts, which was the explicit goal this round of cleanup was done to unblock.

---

## 4. Alternatives considered

| Alternative | Why rejected |
|---|---|
| **Keep `--architecture` a literal (`amd64`) instead of deriving it from the build host** | This was the original plan (§2 item 1 before item 5's testing) — rejected once `make test-packaging-linux-vm` on an arm64 Mac showed the literal doesn't hold for the one host class this tooling exists to support. A derived value costs nothing on amd64 (still defaults to `amd64` there) and fixes the arm64 case for free. |
| **Add `%ghost`-equivalent tracking for `/etc/pf9-byohost-agent.service.d/` instead of fixing the shell test** | The RPM ADR already rejected the symmetric idea (`%ghost`) for the same reason: it'd mean tracking a second, package-format-specific list of generated paths that has to be kept in sync with what the scripts actually create — exactly the kind of drift this ADR and the RPM one both exist to remove. The shell test just had the wrong `[ ... ]` primary; fixing that is strictly simpler than adding a tracking mechanism. |
| **Leave `after-remove.sh` without a shebang since it currently "works"** | Working via an implementation-defined `execvp` fallback isn't the same as being correct, and it's the one file in this trio that's inconsistent with its siblings for no reason — there's no cost to matching them. |
| **Fix the log path by adding the missing directory to `after-install.sh` instead of changing `before-remove.sh`'s path** | Doesn't fix the actual bug: `before-remove.sh` should write into the same log directory every other log in this package uses, not a different one that happens to also get created. Also wouldn't handle uninstall-after-partial-install, where `after-install.sh` may not have run to completion. |
| **Use QEMU/binfmt emulation in the test container instead of building packages for the host's real architecture** | Would let the amd64-only pipeline stay as-is and paper over the mismatch at test time instead of fixing it. Rejected because the underlying bug (package metadata not matching package contents) is real independent of how it's tested, and because emulation for the *entire* test container is a heavier, slower dependency than making the Makefile build for the arch it's already running on. |
| **Keep the `-linux-$(GOARCH)` suffix on the installed binary path, and thread `$(PACKAGE_GOARCH)` through the hook scripts and RPM spec to reference it dynamically** | Would work, but means every one of `after-install.sh`, `before-remove.sh`, the RPM spec, and the systemd unit needs its own way to learn the current package's architecture at install/uninstall time (env var, generated file, `uname`-sniffing) just to reconstruct a filename the package's own `Architecture` field already makes redundant. Dropping the suffix removes the need for that machinery entirely — one arch-neutral literal path, everywhere. |

---

## 5. Testing plan

**Status: done.** Extended the existing `test/e2e/packaging/deb_install_test.go` and
`rpm_install_test.go` (from the RPM ADR's §5) rather than add a new suite — new assertions on an
existing flow, not a new flow:

- **Architecture** (deb): after building, `dpkg-deb -f <deb> Architecture` must equal
  `runtime.GOARCH` of the test process itself — the build and the test both run natively on
  whatever host/container invoked `go test`, so this holds on both amd64 and arm64 without the
  test needing to know which one it's on.
- **Conf-directory cleanup** (deb): after `dpkg -r`, assert `/etc/pf9-byohost-agent.service.d` is
  gone (`test -e` exit code, same pattern the suite already uses for the binary and unit file) —
  the regression test for finding 2, and the gap that let it ship unnoticed.
- **Uninstall log path** (deb): after `dpkg -r`, assert
  `/var/log/pf9/byoh/byoh-agent-uninstall.log` exists and contains the completion message.
- **Binary path rename**: both suites' existing file-ownership assertions
  (`/binary/pf9-byoh-hostagent-linux-amd64`) updated to the new arch-neutral
  `/binary/pf9-byoh-hostagent` — this alone would have caught item 5 as a regression against the
  old hardcoded path if it hadn't been an intentional rename.

Validated end to end with `make test-packaging-linux-vm` on an Apple Silicon Mac: both the RPM and
deb specs pass, each producing and installing an aarch64/arm64 package with a working
`EnvironmentFile` and enabled service — the actual goal this cleanup exists to unblock.

---

## 6. Evidence index

| Claim | Location |
|---|---|
| `fpm` invocation originally tagged the package `Architecture: all` | `Makefile` (`build-host-agent-deb`'s `fpm` invocation, pre-fix) |
| Conf-directory cleanup glob only ever matched a directory, which `-f` never matches | `scripts/pf9-byohost-agent-before-remove.sh` (pre-fix); directory created at `scripts/pf9-byohost-agent-after-install.sh` |
| `deb_install_test.go` didn't assert the conf directory was removed | `test/e2e/packaging/deb_install_test.go` (pre-fix; asserted only the binary and unit file post-`dpkg -r`) |
| `after-remove.sh` had no shebang, unlike its siblings | `scripts/pf9-byohost-agent-after-remove.sh` (pre-fix); contrast `after-install.sh`, `before-remove.sh` |
| Uninstall log path differed from every other log path in the same package | `scripts/pf9-byohost-agent-before-remove.sh` vs. `after-install.sh` (pre-fix) |
| `make test-packaging-linux-vm`'s deb spec failed with `package architecture (amd64) does not match system (arm64)` after item 1 alone, on an Apple Silicon Mac | first `make test-packaging-linux-vm` run this ADR's implementation was tested against |
| `byoh/node:e2e` test container is built natively for whatever host/Podman-machine arch is running the test | `docker inspect byoh/node:e2e` → `arm64` on the Apple Silicon Mac this was tested on |
| RPM suite "passed" on the same run only because `rpmbuild` infers `Architecture` from the build host while the staged binary was still hardcoded amd64 — a hidden version of the same bug | pre-fix `$(COMMON_SRC_ROOT)`, shared by both `build-host-agent-rpm` and `build-host-agent-deb` |
| `PACKAGE_GOARCH` derivation and `$(COMMON_SRC_ROOT)`'s independent binary build | `Makefile` |
| Binary staged at arch-neutral `/binary/pf9-byoh-hostagent`, referenced identically by both hook scripts, the RPM spec, and the systemd unit | `scripts/pf9-byohost-agent-after-install.sh`, `scripts/pf9-byohost-agent-before-remove.sh`, `scripts/pf9-byohost.spec`, `service/pf9-byohostagent.service` |
| `make test-packaging-linux-vm` passes both specs after all fixes, on an Apple Silicon Mac | test run: `Ran 2 of 2 Specs ... SUCCESS! -- 2 Passed \| 0 Failed` |
