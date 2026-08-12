# ADR: Clean up `pf9-byohost.spec`

**Status:** Implemented — all §2 decision items done, RPM and deb both have a passing install/uninstall test in CI (§5)
**Date:** 2026-08-11
**Deciders:** TBD
**Related:** none

---

## 1. Context

`scripts/pf9-byohost.spec` was added complete, in a single commit
(`2138f5e`, "Add byohost-agent systemd service , build and push pf9-byohost-agent.deb"), and has
never been touched since (`git log --follow -- scripts/pf9-byohost.spec` shows exactly one
commit). It backs `make build-host-agent-rpm` (`Makefile:290-297`), which is not wired into any
CI workflow — `.github/workflows/draft-release.yaml` has no `rpm`/`deb`/`byohost` reference. So
nothing has exercised this path since it was written, and it shows.

**Findings, in the order they surfaced:**

- **A dead, build-breaking `%files` entry.** `/namespace` is listed in `%files`
  (`scripts/pf9-byohost.spec:43`) but nothing creates it: not `%install`
  (`scripts/pf9-byohost.spec:22-30`), not the Makefile's staging step for the RPM source tree
  (`Makefile:303-311`), not any Go code (`agent/`, `cmd/byohctl/`) or Makefile target anywhere in
  the repo. `rpmbuild` fails hard when `%files` lists a path absent from the buildroot, so this
  spec should not be able to build successfully as written — the only reason nobody's hit it is
  that nothing in CI ever tries.

- **A redundant manual removal.** `%preun` (`scripts/pf9-byohost.spec:87-144`) does
  `rm -f /binary/pf9-byoh-hostagent-linux-amd64`. That path is declared in `%files`
  (`scripts/pf9-byohost.spec:40-41`, itself listed twice), so it is RPM-owned — `rpm -e`
  deletes every file it owns automatically. The shell block duplicates work RPM was already
  about to do.

- **A self-inflicted manual removal.** `%preun` also removes
  `/etc/systemd/system/pf9-byohost-agent.service`. That path is *not* in `%files` — only
  `/lib/systemd/system/pf9-byohost-agent.service` is (`scripts/pf9-byohost.spec:42`). The
  `/etc/systemd/system` copy is created out-of-band by a `cp` in both `%install`
  (`scripts/pf9-byohost.spec:30`) and `%post` (`scripts/pf9-byohost.spec:71`), so RPM has no
  record of it and can't clean it up on its own. The manual `rm` exists only because of the
  earlier choice to stage the unit at `/lib/systemd/system` and then copy it to `/etc` rather
  than packaging it at the path systemd actually reads it from.

- **`%post`'s `BOOTSTRAP_KUBECONFIG` check is disconnected from how the service actually gets
  that value.** `%post` (`scripts/pf9-byohost.spec:63-85`) checks `$BOOTSTRAP_KUBECONFIG` in its
  own shell environment, then runs `systemctl start`. But the systemd unit
  (`service/pf9-byohostagent.service:12-13`) reads `BOOTSTRAP_KUBECONFIG` from
  `EnvironmentFile=/etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf` — a file `%post`
  never creates. Two independent problems follow: (a) the env var has to be exported by whoever
  invokes `rpm -i`, which `sudo`'s default `env_reset` strips unless the caller knows to use
  `sudo -E` or `sudo VAR=... rpm -i ...`, so the check fails in the common case regardless of
  whether a kubeconfig exists; and (b) even when it passes, the value is discarded — the service
  starts with an empty `EnvironmentFile` either way. Compare the deb path's
  `pf9-byohost-agent-after-install.sh:25-32`, which *does* write that conf file (from
  `/root/.byoh/config`'s `namespace:` key and `/root/.byoh/region`, not from an env var) — so the
  RPM path is not just fragile, it's missing a step the deb path already has.

- **The RPM scriptlets and the deb `fpm` hook scripts duplicate the same lifecycle logic and
  have already drifted apart.** RPM's `%pre`/`%post`/`%preun`/`%postun` are inline shell blocks
  because that's how `rpmbuild` scriptlets work; the deb build instead uses `fpm`'s
  `--after-install` / `--before-remove` / `--after-remove` flags pointing at standalone files
  (`Makefile:317-327`): `pf9-byohost-agent-after-install.sh`, `-before-remove.sh`,
  `-after-remove.sh`. Nobody kept the two copies in sync:
  - `%post` is missing the `/root/.byoh` setup and the `NAMESPACE`/`REGION` → conf-file
    generation that `pf9-byohost-agent-after-install.sh:25-32` does (see above).
  - `%preun`'s uninstall log path is `/var/log/pf9/byoh/byoh-agent-uninstall.log`
    (`scripts/pf9-byohost.spec:92`); `pf9-byohost-agent-before-remove.sh:6` uses
    `/var/log/pf9/byoh-agent-uninstall.log` — missing the `byoh/` segment. One of these is wrong.
  - `pf9-byohost-agent-before-remove.sh:58-100` additionally cleans up `/etc/pf9-byohost*`,
    `/root/.byoh/config`, `/root/.byoh/packages`, `/root/.byoh`, and `/usr/bin/byohctl`. `%preun`
    does none of this.

  Net effect: an RPM install and a deb install of the same version behave differently today —
  worse, RPM installs likely don't get a working `BOOTSTRAP_KUBECONFIG` at all.

**More bugs surfaced once §2's fixes were actually attempted against a real `rpmbuild`/`rpm -i`**,
confirming the "never successfully built, never exercised" read above — none of these were
visible from reading the spec alone:

- **No `Version`/`Release` fields at all.** Both are mandatory RPM header fields; `rpmbuild`
  refuses to build without them. Fixed by wiring up `_githash`, a macro the Makefile already
  passed into every `rpmbuild` invocation (`Makefile:423` at the time) but the spec never
  referenced.
- **`RPMBUILD_DIR` and `RPM_SRC_ROOT` were the same directory** (`Makefile:396-398` at the time).
  Since `_topdir` (rpmbuild's own `BUILD`/`RPMS`/`SRPMS` scratch tree) and `_src_dir` (the payload
  to package) pointed at the same path, `%install`'s `cp -r $SRC_DIR/* $RPM_BUILD_ROOT` tried to
  copy `_topdir`'s own `BUILD/` into itself. Fixed by giving `_topdir` its own directory.
- **`scripts/sign_packages.sh` was checked in non-executable**, and the Makefile line invoking it
  had a redundant `./` prefixed onto an already-absolute path (`./$(AGENT_SRC_DIR)/scripts/...`),
  producing a malformed path. Both fixed.
- **`%post`'s `BOOTSTRAP_KUBECONFIG` file-existence check was single-quoted** —
  `[ ! -f '$BOOTSTRAP_KUBECONFIG' ]` never expands the variable, so the check was always true and
  `%post` failed unconditionally regardless of whether the real file existed. Found only by
  actually writing an install test against it (§5). Fixed.

---

## 2. Decision

Rewrite `pf9-byohost.spec` so its scriptlets stop reimplementing what RPM already does, stop
duplicating logic that already exists (and has drifted) in the deb hook scripts, and stop
carrying dead entries:

1. **[x] Done. Delete `/namespace` from `%files`.** It corresponds to nothing; removing it is what
   makes the spec buildable at all.

2. **[x] Done. Delete the manual binary `rm` from `%preun`.** RPM removes `%files`-owned paths
   automatically on erase; the shell has nothing to do here.

3. **[x] Done. Package the systemd unit at the path it's actually read from, and drop the copy
   step.** Changed `%install` and the `Makefile` staging step to place the unit directly at
   `/etc/systemd/system/pf9-byohost-agent.service` in the buildroot and list *that* path in
   `%files`, instead of staging at `/lib/systemd/system` and `cp`-ing to `/etc` in both
   `%install` and `%post`. RPM now owns it and removes it on erase; the manual `rm` in `%preun`
   for this path is gone too.

4. **[x] Done, as a side effect of item 5.** `%post` no longer gates on `$BOOTSTRAP_KUBECONFIG` at
   all — converging onto `pf9-byohost-agent-after-install.sh` (item 5) replaced the whole inline
   check with that script's actual behavior: unconditionally generating
   `/etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf` from `/root/.byoh/config`'s
   `namespace:` key and `/root/.byoh/region`. Verified by `rpm_install_test.go` asserting the conf
   file's content after install. Whether the referenced kubeconfig file actually exists is, as
   intended, the agent binary's problem at process start, not something checked at install time.

5. **[x] Done. Converge the RPM scriptlets onto the same shell files the deb build already
   uses**, via `rpmbuild`'s `-f <file>` scriptlet form: `%post -p /bin/bash -f
   %{_scripts_dir}/pf9-byohost-agent-after-install.sh`, likewise for `%preun`/`%postun`
   (`_scripts_dir` is a new Makefile-supplied macro, `Makefile`). `-p /bin/bash` matters — these
   scripts have bash-specific expectations and `-f` alone would default to `/bin/sh`. **`%pre` is
   deliberately NOT converged**: there is no deb-side `--before-install` hook at all, and the
   Rocky/`libcgroup-tools` check is inherently RPM-database-specific (`rpm -q`), so there's
   nothing on the deb side to converge it with — it stays inline, with a comment explaining why.
   Where RPM and deb had already diverged (§1) — the uninstall-log path, and the extra cleanup
   `before-remove.sh` does that `%preun` didn't — **the deb scripts won**, exactly as decided:
   converging onto them pulled that fuller behavior onto the RPM path too, not the other way
   around. Validated end to end: `make test-packaging-linux-vm` (RPM) and a manual `dpkg
   -i`/`dpkg -r` cycle against `byoh/node:e2e` (deb) both install, enable the service, generate the
   conf file, and clean up completely on removal.

6. **[x] Done (no action needed). Leave the generated conf file and log files out of `%files`
   entirely — no `%ghost`.**
   `%ghost` would let RPM own and auto-remove them too, but that means tracking a second,
   RPM-specific list of generated paths in the spec that has to be kept in sync with whatever
   `pf9-byohost-agent-after-install.sh`/`-before-remove.sh` actually create — another place for
   the spec and the shared scripts to quietly drift apart, which is the exact problem this ADR is
   cleaning up. Simpler to keep one mechanism: the shared scripts (§2.5) already create and clean
   these up themselves, so that's the only place this logic lives, for both package formats.

Net shape, now implemented: `%post`/`%preun`/`%postun` are thin `-f` references to the shared
scripts (`%pre` stays inline, per item 5's note above); `%files` lists only real, RPM-owned paths
(binary, unit file) — nothing generated, nothing `%ghost`. RPM and deb installs behave identically
because they run the same scripts, except for `%pre`'s Rocky-specific dependency check, which has
no deb equivalent to converge with.

---

## 3. Consequences

**Positive.** One source of truth for install/uninstall behavior instead of two hand-copied and
already-diverged ones. `make build-host-agent-rpm` becomes buildable again. RPM-installed hosts
get a working `BOOTSTRAP_KUBECONFIG`/`NAMESPACE`/`REGION` `EnvironmentFile`, matching deb
installs, instead of starting the service with none. Less shell overall — RPM's own package
manager absorbs work three separate `%preun` blocks were reimplementing by hand.

**Negative / cost.** Adopting the deb scripts as authoritative (§2.5) means the RPM path gains
behavior it never had — `/etc/pf9-byohost*`, `/root/.byoh/config`, `/root/.byoh/packages`,
`/root/.byoh`, and `/usr/bin/byohctl` cleanup on uninstall — which should be called out in the
release notes for whoever operates RPM hosts today, since it's a behavior change, not just a
bugfix, even though it's the intended one.

**Risk if not done.** `/namespace` keeps the RPM target unbuildable the moment anyone tries it.
RPM and deb installs keep silently behaving differently, and RPM hosts likely keep starting the
agent with an empty kubeconfig path.

---

## 4. Alternatives considered

| Alternative | Why rejected |
|---|---|
| **Leave the spec as-is** | This is the status quo this ADR is against: a build-breaking dead `%files` entry, redundant/self-inflicted manual cleanup, and a `%post` check that's disconnected from how the service actually receives its kubeconfig. |
| **Just delete the broken `BOOTSTRAP_KUBECONFIG` check without replacing it** | Would silently drop even the appearance of validation while leaving the real bug (the conf file is never written) in place. The fix is generating the file, not removing a guard around a step that was never happening anyway. |
| **Package the shared scripts inside the RPM payload and have `%post`/`%preun` `.`-source them at runtime** | Rejected in favor of `-f <file>`, which inlines the script content at build time. Runtime-sourcing would ship extra standalone-executable files as part of the package payload for no benefit RPM scriptlets don't already give for free via `-f`. |
| **Leave the `/lib/systemd/system` → `/etc/systemd/system` copy in place and just add the missing `rm` to `%preun` instead of removing the copy step** | Fixes the symptom, not the cause — RPM still wouldn't own the file, so any future change to this area reintroduces the same class of manual-cleanup bug. Packaging the unit at its real path removes the need for the workaround entirely. |
| **Fully merge the RPM and deb scripts into one script invoked by both `-f` and `fpm`'s hook flags** | This is effectively what §2.5 does — `-f` and `fpm --after-install` both just need a path to a shell file, so the same `pf9-byohost-agent-after-install.sh` etc. can serve both without a separate merge step. Not treated as a separate alternative; it's the chosen mechanism. |

---

## 5. Testing plan

**Status: done, both formats.** `test/e2e/packaging/rpm_install_test.go` and the new
`deb_install_test.go` both build their package, install it in a real systemd container, assert
file ownership, assert the `EnvironmentFile` conf got generated, assert the service is enabled,
uninstall, and assert cleanup — wired into `.github/workflows/e2e.yml` as the `packaging` job,
running in parallel with `e2e` on the same triggers. The deb half was deliberately deferred
earlier in this ADR's implementation (to keep the first PR reviewable) until §2 items 4–5 landed;
it's built now that both formats actually run the same scripts and have something meaningful to
share.

No manual step counts as validation for this ADR — every claim below has to be asserted by code
that runs unattended and fails the build on a wrong result. Before this work, nothing automated
touched this path at all: `build-host-agent-rpm`/`-deb` weren't in CI, and no existing e2e spec
installed the RPM/deb artifact — `test/e2e/docker_helper.go` and the specs built on it
(`byohost_reuse_test.go`, `cluster_upgrade_test.go`, etc.) all run the agent binary directly inside
a container, never through `rpm`/`dpkg`.

**New, separate suite — not an extension of `test/e2e`'s existing Ginkgo suite.** That suite's
`SynchronizedBeforeSuite` (`test/e2e/e2e_suite_test.go:143-199`) stands up a full kind bootstrap
cluster, a clusterctl local repository, and CAPI providers before any spec runs, and
`make test-e2e` (`Makefile:235-236`) runs the whole `test/e2e` package as one Ginkgo binary with
one shared setup/teardown. Package install/uninstall tests need none of that — only a container
plus `rpm`/`dpkg` — so folding them in would make every packaging run pay for kind-cluster
bootstrap it doesn't need, and couple packaging failures to unrelated cluster-reconciliation
failures in the same run. **Built as** its own package, `test/e2e/packaging` (package
`packaging_test`), with its own `TestPackaging`/`RunSpecs` entrypoint and its own Makefile targets
(`test-packaging`, `test-packaging-linux-vm`). One deviation from the original plan: rather than
exporting and reusing `docker_helper.go`'s `copyToContainer`/`createDockerContainer`, it has its
own small, self-contained `copyFileToContainer`/`execInContainer` helpers (`rpm_install_test.go`)
built directly on the Docker SDK — simpler than replicating `docker_helper.go`'s full
symlink/directory-merge `docker cp` semantics for what's just one file, and it keeps this new
package's only coupling to `test/e2e` being the container images, not its Go internals.

**Container images — mostly reuse, one genuinely new one.** `test/e2e/BYOHDockerFile` already
builds the `byoh/node:e2e` image with `ENTRYPOINT ["/sbin/init"]` (systemd as PID 1) and already
installs `conntrack ethtool socat ebtables` — exactly the deps this spec's `Requires:`/`-d` flags
list. The **deb** install/uninstall test (`deb_install_test.go`) runs against that exact,
already-existing image — `test-packaging`'s Makefile target now depends on
`prepare-byoh-docker-host-image` too, no new Dockerfile needed. The **RPM** side got the one
genuinely new image: `test/e2e/packaging/RockyDockerFile`, mirroring `BYOHDockerFile` on a Rocky
base, matching the `%pre` Rocky check (`scripts/pf9-byohost.spec`).

- **[x] Automated build step**: each spec's own `BeforeSuite`/`It` runs `make build-host-agent-rpm`
  or `make build-host-agent-deb` via `os/exec`, asserted on exit code. This is the automated
  replacement for "does `rpmbuild`/`fpm` even succeed," which before this work it didn't, for four
  independent reasons on the RPM side alone (§1).

- **RPM, implemented** (`rpm_install_test.go`), driven entirely through `docker exec` from Go and
  asserted with Gomega:
  1. Start the Rocky container, `docker exec` the install (`rpm -i`), assert the exit code.
  2. Assert package contents via `rpm -ql`, parsed in Go: exactly the binary and unit file, no
     `/namespace`, nothing else (§2.6).
  3. Assert `/etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf` was generated and contains
     `BOOTSTRAP_KUBECONFIG=`/`NAMESPACE=`/`REGION=` — now that §2 item 4 landed (as a side effect
     of item 5), this is real, not deferred. No stub kubeconfig or env var needed on the `rpm -i`
     call anymore, since `%post` doesn't read `$BOOTSTRAP_KUBECONFIG` at all now.
  4. Asserts `systemctl is-enabled`, not `is-active` as originally planned: the agent gets no real
     cluster to reach (nothing in `/root/.byoh/config`/`region` either, in this test), so the
     long-running process can never reach a stable "active" state here — that's the agent's own
     runtime behavior, not what a packaging test should be asserting. `is-enabled` still confirms
     `%post`'s `systemctl enable` ran.
  5. `docker exec` the uninstall (`rpm -e`) and assert binary and unit file gone (RPM's own
     removal, not shell).
  6. Wired into CI as its own `packaging` job (`.github/workflows/e2e.yml`), same triggers as
     `e2e`, running in parallel.

- **[x] Done. Deb** (`deb_install_test.go`), same shape as RPM but `dpkg -i`/`dpkg -L`/`dpkg -r`
  against `byoh/node:e2e`. One real difference worth keeping, not "fixing": `dpkg -L` also lists
  the directory entries `fpm` preserved from the staged source tree (`/binary`,
  `/etc/systemd/system`, `/usr/share/doc/...`), unlike `rpm -ql`, which only lists the two files
  `%files` actually declares — so this assertion checks the two files are present
  (`ContainElements`) rather than an exact list (`ConsistOf` as the RPM spec uses). This test is
  also what caught a real regression before it shipped: converging `%post`/`%preun`/`%postun`
  onto the shared scripts (item 5) required staging the systemd unit at `/etc/systemd/system`
  instead of `/lib/systemd/system` (item 3), and that staging step (`Makefile`'s
  `COMMON_SRC_ROOT`) is shared by both `build-host-agent-rpm` and `build-host-agent-deb` — so the
  RPM-focused fix had silently broken `pf9-byohost-agent-after-install.sh`, which still checked
  for the old path and would `exit 1` on every deb install. Caught by manual investigation before
  this spec existed; fixed by deleting the now-dead check-and-copy block (`dpkg` already installs
  the unit at its packaged path — nothing left to copy, same insight as the original RPM fix).
  This spec now guards against that class of regression recurring silently.

- **[x] Done. CI wiring was part of this plan, not a follow-up**, and landed with the RPM suite: a
  `packaging` job in `.github/workflows/e2e.yml`, same triggers `e2e` uses — push to `main` and
  every non-draft PR — running in parallel with, not serialized into, `e2e`, since the two suites
  test unrelated concerns with unrelated (and expensive) setup costs. Without this wiring it'd
  just be a script nobody runs, which is the exact failure mode this ADR exists because of.

---

## 6. Evidence index

| Claim | Location |
|---|---|
| `/namespace` in `%files` corresponds to nothing created anywhere | `scripts/pf9-byohost.spec:43`; `scripts/pf9-byohost.spec:22-30`; `Makefile:303-311` |
| Spec file added whole in one commit, never modified since | `git log --follow --oneline -- scripts/pf9-byohost.spec` → `2138f5e` only |
| `build-host-agent-rpm`/`-deb` not exercised by CI | `.github/workflows/draft-release.yaml` (no `rpm`/`deb`/`byohost` reference) |
| Binary path is RPM-owned via `%files`, so manual `%preun` removal is redundant | `scripts/pf9-byohost.spec:40-41,87-144` (removal block) |
| `/etc/systemd/system/...` service copy is untracked by `%files`, created via `cp` in `%install`/`%post` | `scripts/pf9-byohost.spec:30,42,71` |
| Systemd unit reads `BOOTSTRAP_KUBECONFIG` from `EnvironmentFile`, not from install-time shell env | `service/pf9-byohostagent.service:12-13` |
| RPM `%post` checks `$BOOTSTRAP_KUBECONFIG` but never writes the `EnvironmentFile` conf | `scripts/pf9-byohost.spec:63-85` |
| Deb `after-install.sh` writes that same conf file, keyed off `/root/.byoh/config`/`region`, not an env var | `scripts/pf9-byohost-agent-after-install.sh:25-32` |
| Deb hooks wired via `fpm --after-install`/`--before-remove`/`--after-remove` | `Makefile:317-327` |
| Uninstall log path mismatch between `%preun` and `before-remove.sh` | `scripts/pf9-byohost.spec:92` vs. `scripts/pf9-byohost-agent-before-remove.sh:6` |
| `before-remove.sh` cleans up more than `%preun` does (conf files, `/root/.byoh`, `byohctl`) | `scripts/pf9-byohost-agent-before-remove.sh:58-100` |
| RPM build invocation and staging | `Makefile:290-297` (rpmbuild), `Makefile:303-311` (`COMMON_SRC_ROOT`) |
| Existing e2e image already runs systemd as PID 1 and already has this spec's runtime deps | `test/e2e/BYOHDockerFile:1-13` |
| Existing e2e suite's shared setup stands up a full kind bootstrap cluster before any spec runs | `test/e2e/e2e_suite_test.go:143-199` |
| `make test-e2e` runs the whole `test/e2e` package as one Ginkgo binary with shared setup/teardown | `Makefile:235-236` |
| CI already runs on every push to `main` and every non-draft PR — no separate schedule needed for a new job | `.github/workflows/e2e.yml:5-20`; `0809a31` ("ci: rearchitect and streamline CI") |
| §2 items 1–3 and 6 implemented; `/namespace`, redundant `%preun` removals, and the `/lib`→`/etc` copy dance are gone | `af243dc` ("fix(scripts): Make pf9-byohost RPM packaging actually build") |
| Missing `Version`/`Release`, self-referential `_topdir`/`_src_dir`, non-executable/malformed `sign_packages.sh` — found only by actually running `rpmbuild` | commit `af243dc`; `scripts/pf9-byohost.spec`; `Makefile` (`RPMBUILD_DIR`, `PF9_BYOHOST_RPM_FILE`) |
| `%post`'s single-quoted `$BOOTSTRAP_KUBECONFIG` check never expanded — found by writing the install test | `69426e5` ("test(packaging): Add an install/uninstall test for the pf9-byohost RPM") |
| RPM install/uninstall suite implemented and passing, deb explicitly deferred | `test/e2e/packaging/rpm_install_test.go`; `test/e2e/packaging/RockyDockerFile` |
| Packaging CI job implemented, using the same `go-version` reusable job the `e2e` job sources from | `.github/workflows/e2e.yml` (`packaging` job, `needs: go-version`) |
| §2 items 4–5 implemented: `%post`/`%preun`/`%postun` converged onto the deb scripts via `-p /bin/bash -f %{_scripts_dir}/...`; `%pre` left inline (no deb equivalent) | `scripts/pf9-byohost.spec`; `Makefile` (`_scripts_dir` define) |
| Converging onto `/etc/systemd/system` staging (item 3) silently broke the deb build's `after-install.sh`, which still checked the old `/lib/systemd/system` path — caught only by manual investigation, no test existed | `scripts/pf9-byohost-agent-after-install.sh` (dead check-and-copy block removed) |
| RPM test's conf-file assertion (previously deferred) now implemented and passing, since `%post` actually generates it | `test/e2e/packaging/rpm_install_test.go` |
| Deb install/uninstall now has a permanent Ginkgo spec, reusing `byoh/node:e2e`; `test-packaging` depends on `prepare-byoh-docker-host-image` to build it | `test/e2e/packaging/deb_install_test.go`; `Makefile` (`test-packaging` target) |
| `dpkg -L` lists directory entries `rpm -ql` doesn't (fpm preserves the staged source tree) — deb's file-ownership assertion uses `ContainElements`, RPM's uses `ConsistOf` | `test/e2e/packaging/deb_install_test.go` vs. `rpm_install_test.go` |
