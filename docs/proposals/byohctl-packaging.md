# ADR: Package `byohctl` inside the agent deb/rpm so the OS package manager upgrades it

**Status:** Proposed — not yet accepted
**Date:** 2026-08-11
**Deciders:** TBD
**Related:** [pf9-byohost-spec-cleanup-adr.md](pf9-byohost-spec-cleanup-adr.md) (the rpm side of
this change lands on top of that rewrite, not before it — see §6); onboarding flow docs
([onboarding-pcdctl.md](onboarding-pcdctl.md), [onboarding-adr.md](onboarding-adr.md)) describe a
curl-then-onboard flow this ADR assumes but does not itself redesign — kept independent
deliberately, see §4.

---

## 1. Context

### 1.1 There is currently no upgrade path for `byohctl` once it's on a host

`byohctl` is built and distributed as a bare binary, not a package: `build-byohctl.yml` builds it
with `cmd/byohctl && make build-all` and pushes the raw binaries to
`quay.io/platform9/cluster-api-provider-bringyourownhost/byohctl` via `imgpkg`
(`cmd/byohctl/Makefile:57,60-65`) — an OCI artifact, not a repo a package manager tracks. Whatever
puts a copy on an operator's or host's disk (today: however it's fetched from that registry; the
onboarding proposal's §4 describes a future `curl | sudo bash` variant) is a one-time copy with no
follow-up mechanism. Nothing re-checks or re-fetches it later.

### 1.2 The agent's own packaging already treats `byohctl` as if it belonged inside the package — it just isn't there yet

`scripts/pf9-byohost-agent-before-remove.sh:94-99` removes `/usr/bin/byohctl` on uninstall,
unconditionally, as part of the deb's `--before-remove` hook (wired in
`Makefile:432-434,317` via `fpm --before-remove`). But nothing in the build that produces that
package ever puts a file at `/usr/bin/byohctl`: `$(COMMON_SRC_ROOT)` (`Makefile:416-421`), the
staging root both the deb (`$(DEB_SRC_ROOT)`, `Makefile:426-427`) and rpm
(`$(RPM_SRC_ROOT)`, `Makefile:399-401`) trees are copied from, stages only the agent binary
(`byoh-hostagent-linux-amd64` → `binary/pf9-byoh-hostagent-linux-amd64`) and the systemd unit. The
rpm spec's `%files` (`scripts/pf9-byohost.spec:38-43`) lists only that same agent binary path, the
unit, and the dead `/namespace` entry `pf9-byohost-spec-cleanup-adr.md` §1 already flags for
removal. So today: uninstalling the agent package deletes a `byohctl` that the package never
installed. Someone already assumed `byohctl` would live there and be package-owned; the build
side of that assumption was never finished.

### 1.3 The deb build format makes this cheap; the rpm build format doesn't

The deb is built with `fpm -t deb -s dir ... -C $(DEB_SRC_ROOT)/ .`
(`Makefile:430-439`) — `-s dir` packages *whatever is in the staging directory tree*, verbatim,
with no separate manifest. Dropping a file at `$(COMMON_SRC_ROOT)/usr/bin/byohctl` before the deb
build runs is sufficient; `fpm` picks it up automatically and dpkg then owns and removes it like
any other packaged file.

The rpm build has no such shortcut: `%files` (`scripts/pf9-byohost.spec:38-43`) is a hand-maintained
list, and `rpmbuild` fails the build if a staged file isn't declared there (the same failure mode
`pf9-byohost-spec-cleanup-adr.md` §1 already documents for the reverse case — `/namespace` declared
but not staged). Adding `byohctl` to the rpm path means staging it *and* adding a `%files` line.

---

## 2. Decision

Stage `byohctl`'s Linux binary into `$(COMMON_SRC_ROOT)/usr/bin/byohctl` (`Makefile:416-421`),
alongside the existing agent binary and unit copy, so both the deb and rpm builds pick it up from
the same shared staging tree they already use. Concretely:

1. **`$(COMMON_SRC_ROOT)` gains a `byohctl` dependency and a copy step.** It currently depends
   only on `build-host-agent-binary`; add a dependency on `cmd/byohctl`'s
   `build` target (or fetch the already-built artifact from the `byohctl-binaries` CI artifact /
   Quay image — an ordering choice for whoever implements this, not a design one) and
   `cp` the resulting `linux-amd64` binary to `$(COMMON_SRC_ROOT)/usr/bin/byohctl`, `chmod +x`.
2. **Deb side needs nothing else.** `-s dir` packaging means `usr/bin/byohctl` under
   `$(DEB_SRC_ROOT)` becomes part of the payload automatically, dpkg-owned like every other file
   in that tree.
3. **Rpm side needs an explicit `%files` line and buildroot staging**, per §1.3 — add
   `/usr/bin/byohctl` to `scripts/pf9-byohost.spec`'s `%files` (mirroring the existing
   `%attr(0644, root, root) /binary/pf9-byoh-hostagent-linux-amd64` pair, with `0755` instead of
   `0644` since it's an executable) and stage it in `%install` the same way the agent binary is
   staged (`scripts/pf9-byohost.spec:22-30`). This ADR does not attempt the rest of the rpm rewrite
   `pf9-byohost-spec-cleanup-adr.md` is already scoped to do — see §6 on ordering.
4. **Drop the now-redundant manual removal.** Once `/usr/bin/byohctl` is declared in `%files`
   (rpm) and is simply part of the dpkg-owned tree (deb), the package manager removes it on
   uninstall automatically, the same argument
   `pf9-byohost-spec-cleanup-adr.md` §2.2 makes for the agent binary's manual `%preun rm`.
   `scripts/pf9-byohost-agent-before-remove.sh:94-99`'s manual `rm /usr/bin/byohctl` becomes
   dead code and should be deleted, not left as a harmless-but-redundant no-op — leaving it risks
   the same kind of drift `pf9-byohost-spec-cleanup-adr.md` §1 found between the rpm and deb
   scripts.
5. **The curled/initial `byohctl` and the packaged one must resolve to the same path:
   `/usr/bin/byohctl`.** Whatever puts the bootstrap copy on the host before the first `onboard`
   run (today: however it's fetched from Quay; per the onboarding proposal's future §4, a
   `curl | sudo bash` install script) must write it to that exact path, not
   `/usr/local/bin` or anywhere else. If the two differ, `PATH` order decides which `byohctl` a
   later invocation finds — on Debian/Ubuntu's default `PATH`, `/usr/local/bin` is searched
   *before* `/usr/bin`, so a curl script that defaults to `/usr/local/bin/byohctl` would silently
   keep shadowing the packaged, upgradeable copy forever. Using one path removes the ambiguity
   entirely: the package's install step overwrites the exact file the bootstrap copy created.

### 2.1 Why overwriting a running binary's own file is safe

`byohctl onboard` is the process invoking `dpkg -i`/`rpm -i`, and the package it installs
now contains a new copy of `byohctl` itself at the path the running process was loaded from. This
is safe on Linux by construction, not by luck: `dpkg`/`rpm` install a new file by unlinking the old
directory entry and creating a new inode (or renaming a newly-written file into place) — they do
not overwrite the running executable's pages. The already-executing `onboard` process holds its
original inode open via the kernel and finishes running unaffected; only the *next* invocation of
`byohctl` from the shell resolves the new file. No special handling is required on `byohctl`'s
side.

---

## 3. Consequences

**Positive.** `byohctl` upgrades on the same cadence and through the same channel as the agent
package itself — whatever mechanism already gets a host onto a newer `pf9-byohost-agent`
package (unattended-upgrades, Ansible, fleet tooling) now also carries `byohctl` forward, with no
separate re-curl step and no new update mechanism to build or maintain. It also finishes an
assumption the packaging already half-made: `before-remove.sh` treating `/usr/bin/byohctl` as
package-owned becomes actually true instead of accidentally deleting a file the package never
installed.

**Negative / cost.** A version-skew window exists between whatever `byohctl` binary bootstrapped
the very first `onboard` call and the one the package it just installed carries — normally
harmless since `onboard`'s job (authenticate, then install the package) is unlikely to change
shape between adjacent versions, but worth noting rather than assuming away. The rpm side requires
touching `scripts/pf9-byohost.spec`, which `pf9-byohost-spec-cleanup-adr.md` is independently
rewriting — see §6 on sequencing so the two changes don't collide. This ADR only answers *how*
new bytes reach the host; it does not create or change any policy for *when* a host's package gets
upgraded (fleet cadence, automation) — that remains whatever answers the equivalent question for
the agent package today.

**Risk if not done.** `byohctl` stays a one-shot artifact with no upgrade path other than an
operator remembering to re-run whatever fetched it initially, on every host, indefinitely — a
divergent-fleet-versions problem with no in-repo mechanism to close it. The `before-remove.sh`
dead-file-removal bug (§1.2) also persists regardless.

---

## 4. Alternatives considered

| Alternative | Why rejected (or: why it might actually be the right call) |
|---|---|
| **`byohctl` self-checks its version and tells the operator to re-curl** | Cheaper to build (a version comparison against a known-good release, no packaging change) and doesn't touch `scripts/pf9-byohost.spec` at all. Rejected as the primary mechanism because it's advisory only — nothing forces the re-curl to happen, so it doesn't actually close the divergent-fleet-versions gap, only surfaces it. Could still be worth adding on top of this ADR as a cheap early-warning check, independent of whether packaging lands. |
| **A separate `byohctl upgrade` self-update command** (downloads and swaps its own binary, à la many CLI tools) | Considered and rejected for the same reason [agent-self-upgrade-adr.md](agent-self-upgrade-adr.md) §4 rejects agent in-process self-upgrade: it bakes a specific download/verify/swap mechanism into the binary that has to upgrade itself, so a bug in that code path can't be fixed by shipping a new version — the broken code is what would have to run the fix. Packaging avoids this: the file-replacement logic lives in `dpkg`/`rpm`, software this repo doesn't maintain and doesn't need to get right itself. |
| **Leave `byohctl` unpackaged, treat re-curling as the accepted operator workflow** | The status quo. Not clearly wrong for a small fleet with disciplined operators, same caveat [agent-self-upgrade-adr.md](agent-self-upgrade-adr.md) §4 raises for the analogous agent-upgrade question — but it means every host's `byohctl` version is whatever was current the day someone last ran the bootstrap step there, with nothing to notice or correct drift. |
| **Package `byohctl` as its own separate deb/rpm rather than folding it into `pf9-byohost-agent`** | Would decouple `byohctl`'s release cadence from the agent's, which may be desirable if they diverge in practice. Not chosen here because `before-remove.sh` (§1.2) already assumes single-package lifecycle-coupling with the agent, and splitting them now would mean *also* deciding a new package's naming, signing, and CI wiring — a larger change than this ADR's scope. Worth revisiting if the agent and `byohctl` release cadences turn out to need independence. |

---

## 5. Testing plan

No existing e2e spec installs the rpm/deb artifact at all — `pf9-byohost-spec-cleanup-adr.md` §5
already establishes this and proposes the harness (disposable systemd-capable containers, install
via `docker exec`, assert via `rpm -qlp`/`dpkg -c`). This ADR's testing rides on that same harness
rather than inventing a second one:

- **Build-time assertion:** `make build-host-agent-deb build-host-agent-rpm` succeeds and the
  resulting package's file list (`dpkg -c` / `rpm -qlp`) includes `/usr/bin/byohctl` with the
  executable bit set.
- **Install:** using the `pf9-byohost-spec-cleanup-adr.md` §5 harness, install the package in a
  disposable container and assert `/usr/bin/byohctl --version` runs and prints the version the
  package was built with (not whatever bootstrap copy might already be on `PATH`).
- **Upgrade:** install version N, record `byohctl --version` and its inode (`stat -c %i`), install
  version N+1 over it, assert the version string changed and the file is a new inode — the
  automated version of §2.1's safety claim, not just an assumption.
- **Uninstall:** assert `/usr/bin/byohctl` is gone after `dpkg -r`/`rpm -e`, via the package
  manager's own removal (no manual `rm` block left to do it — confirms §2 step 4 was actually
  applied, not just proposed).

---

## 6. Sequencing against `pf9-byohost-spec-cleanup-adr.md`

That ADR is already rewriting `scripts/pf9-byohost.spec`'s `%files`, `%install`, and scriptlets for
unrelated reasons (the dead `/namespace` entry, the missing `BOOTSTRAP_KUBECONFIG` conf-file
write, converging `%pre`/`%post`/`%preun` onto the shared deb hook scripts via `-f`). Land that
ADR's rpm rewrite first, then add this ADR's `/usr/bin/byohctl` `%files` line and staging step on
top of the cleaned-up spec — implementing both against the current, already-known-broken spec
risks a merge collision on the same handful of lines for no benefit. The deb side has no such
dependency (`before-remove.sh`'s redundant `rm` deletion, §2 step 4, is independent of that ADR's
scope) and can land on its own.

---

## 7. Evidence index

| Claim | Location |
|---|---|
| `byohctl` built and pushed as raw binaries to Quay via `imgpkg`, not packaged | `.github/workflows/build-byohctl.yml`; `cmd/byohctl/Makefile:57,60-65` |
| Deb hook already removes `/usr/bin/byohctl` on uninstall | `scripts/pf9-byohost-agent-before-remove.sh:94-99` |
| That hook is wired via `fpm --before-remove` | `Makefile:432-434` |
| Shared staging root only copies the agent binary and systemd unit today | `Makefile:416-421` |
| Deb build packages the entire staging tree verbatim (`-s dir`) | `Makefile:430-439` |
| Rpm `%files` is a hand-maintained list; nothing byohctl-related in it today | `scripts/pf9-byohost.spec:38-43` |
| Rpm staging root, shared with deb via `$(COMMON_SRC_ROOT)` | `Makefile:399-401,426-427` |
| Dead `/namespace` `%files` entry and other rpm-spec issues this ADR builds on top of | [pf9-byohost-spec-cleanup-adr.md](pf9-byohost-spec-cleanup-adr.md) §1 |
| In-process self-upgrade rejected for the agent on the same "can't fix a bug in the code that has to run the fix" ground this ADR reuses for `byohctl` | [agent-self-upgrade-adr.md](agent-self-upgrade-adr.md) §4 |

---

## 8. Open questions

1. **Does `cmd/byohctl`'s own version need to move in lockstep with the agent package's
   version**, or should the Makefile pin a specific `byohctl` build (e.g. by tag/commit) into a
   given agent package release independently? `Makefile:22,28`'s shared `tag` target already
   gives every artifact — agent, `byohctl` — the same git-derived version string, which suggests
   lockstep is already the implicit intent; not confirmed against release practice.
2. **Where does the `cmd/byohctl` binary consumed by `$(COMMON_SRC_ROOT)` come from at build
   time** — a local `make -C cmd/byohctl build`, or pulling the already-published Quay artifact
   (`cmd/byohctl/Makefile:57`)? Affects whether `build-host-agent-deb`/`-rpm` need network access
   to Quay or can run fully offline against a freshly-checked-out tree.
3. **arm64.** `cmd/byohctl/Makefile:build-all` already produces `linux-arm64`
   (per this repo's recent arm64 installer-bundle work per git history); does the agent
   package build gain an arm64 variant at the same time, or does `byohctl`-in-package stay
   amd64-only until the agent package itself goes multi-arch?
4. **Does the curl-based bootstrap step (however it's ultimately implemented — see the
   onboarding proposal's §4) get updated to target `/usr/bin/byohctl` specifically**, closing
   §2 step 5's path-parity requirement? This ADR states the requirement; it doesn't own the script
   that has to satisfy it.
