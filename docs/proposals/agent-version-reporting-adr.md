# ADR: Self-reported agent version and machine identity on ByoHost

**Status:** Proposed — not yet accepted
**Date:** 2026-08-10
**Deciders:** TBD
**Related:** `agent-self-upgrade-adr.md` (an optional follow-on that
depends on the fields this ADR adds — not required to build this one),
`onboarding-adr.md` (§4.5, §8 Q6 — the hostname-identity problem `MachineID`
below is partly aimed at)

> **Scope boundary:** this ADR is observability only. It does not implement, automate, or
> orchestrate agent upgrades in any way, and it does not implement any identity enforcement. How
> a new binary actually gets onto a host — SSH, Ansible, manual reinstall, or the
> controller-driven design in `agent-self-upgrade-adr.md` — is
> entirely out of scope. Its only job is making two facts checkable from the management cluster
> instead of requiring an SSH session per host: what agent version a host is running, and which
> physical/virtual machine it actually is.

---

## 1. Context

- The agent reports its own build version only via a CLI flag: `agent/main.go:100,144-148`
  parses `--version` and prints `version.Get()` (`agent/version/version.go:33-46`,
  ldflags-populated by `hack/version.sh` at build time) to stdout, then exits. It is never sent
  to the API server.
- `ByoHostStatus` (`apis/infrastructure/v1beta1/byohost_types.go:67-96`) has no agent-version
  field of any kind. There is no way, from `kubectl` or any controller, to know what agent
  binary version is running on a given host — the only way to check today is to SSH in and run
  `byoh-hostagent --version` by hand, once per host.
- The existing heartbeat mechanism already answers a related but different question. The agent
  writes `Status.LastHeartbeatTime` every reconcile tick (no more often than `HeartbeatInterval`,
  `agent/reconciler/host_reconciler.go:41,103-109`), and the management-cluster
  `ByoHostReconciler` turns that into the `AgentConnected` condition
  (`controllers/infrastructure/byohost_controller.go:31,94,104-108`;
  `apis/infrastructure/v1beta1/condition_consts.go:84-96`). That tells you **"is the agent
  alive"** — it says nothing about **"is it running the version I expect."** Those are two
  different questions, and today only the first one is answerable without SSH.
- Separately, `ByoHost` has no stable notion of *which machine* it actually is. Its name, its
  CSR's CN, and the certificate identity it registers under all derive from `os.Hostname()`
  (`onboarding-adr.md` §4.5). Hostnames can be changed, are frequently duplicated across
  template- or image-provisioned fleets, and get reused whenever a host cycles back through the
  capacity pool (`test/e2e/byohost_reuse_test.go`, "When BYO Host rejoins the capacity pool").
  Nothing today can tell whether the machine behind a given `ByoHost` quietly changed — this is
  already flagged as an open risk in `onboarding-adr.md` §8, Q6, but nothing addresses it.

**Net effect:** an operator who upgrades a host's agent by hand — or via Ansible, or any other
external means — has no way to confirm the upgrade actually took effect on a given host short of
SSHing in and running `--version`, and separately, has no way to confirm a `ByoHost` still points
at the same underlying machine it originally did. At fleet scale neither is really checkable.

---

## 2. Decision

Add two fields to `ByoHostStatus`, both written by the agent from the exact same reconcile tick
that already writes `LastHeartbeatTime`. Deliberately kept to these two — no `bootID`, no
`kernelVersion`, no duplicating what `HostDetails` or the Node object already cover (§4).

```go
// AgentVersion is the version.GitVersion of the agent binary currently
// running on this host, as reported by the agent itself. The management
// cluster only reads it.
// +optional
AgentVersion string `json:"agentVersion,omitempty"`

// MachineID uniquely identifies the underlying machine this host runs on,
// read from /etc/machine-id (falling back per systemd's machine-id(5)
// convention where absent) and reported by the agent itself. Unlike the
// ByoHost name — derived from os.Hostname(), which can change, collide, or
// be reused across a re-provisioned machine — this does not change unless
// the machine itself is reimaged. It is a fact, not an enforcement
// mechanism: this ADR only surfaces it, it does not compare it against
// anything or reject a mismatch.
// +optional
MachineID string `json:"machineID,omitempty"`
```

Populated from `version.Get().GitVersion` (`agent/version/version.go:33-46`) — the same value
`--version` already prints today, just also written to status instead of only stdout — and from
a new small function reading `/etc/machine-id`, following the exact injectable-reader idiom
`getOperatingSystem` already uses for `/etc/os-release` (`agent/registration/host_registrar.go:159-174`),
so it's testable the same way without any new mocking machinery.

Nothing else changes. No new `Spec` field, no Secret, no controller, no CRD, and no comparison
logic anywhere — `MachineID` is reported, not acted on; deciding whether or how to *use* a
mismatch (alert, block, nothing) is out of scope and left to `onboarding-adr.md` §8, Q6 or a
future ADR. How a host gets to a new agent version is likewise not this ADR's concern — an
operator upgrades however they already do, and the combination of `AgentVersion` and the
pre-existing `AgentConnected` condition becomes the way to confirm it worked.

---

## 3. Consequences

**Positive.** Minimal and additive — two fields, reusing an execution path that already runs
every reconcile tick and, for `MachineID`, an already-established reading pattern in the same
file. It directly answers the question this whole investigation started from ("how are agent
upgrades validated today?" — currently: they aren't), and separately gives a durable identity
anchor that survives hostname changes and pool reuse. Both are useful independent of any upgrade
activity: `kubectl get byohosts -o custom-columns=...` becomes a fleet-wide version inventory,
and `MachineID` lets something — today a human, later maybe automation — notice when a `ByoHost`
that's supposed to be "the same host" no longer is.

**Negative / cost.** Provides no safety net by itself. An operator running Ansible against every
host at once still has no staging, no blast-radius control, and no automatic halt-on-failure —
`AgentVersion` only tells you *after the fact* whether a given host converged, not whether it's
safe to continue rolling out to the rest of the fleet. That gap is what
`agent-self-upgrade-adr.md` is for, if and when it's built. Likewise,
`MachineID` on its own doesn't *prevent* an identity mix-up, an admission check, or anything
else — it just makes one previously invisible fact visible; deciding whether to build enforcement
on top of it is `onboarding-adr.md` §8, Q6's problem, not this ADR's.

**Risk if not done.** Status quo continues on both counts: "did the upgrade work" stays a manual,
per-host SSH check that doesn't scale, and a `ByoHost` silently pointing at a different machine
than it used to stays undetectable.

---

## 4. Alternatives considered

| Alternative | Why rejected |
|---|---|
| **Do nothing; keep relying on manual `--version` checks over SSH** | This is the current state and the gap this ADR closes — doesn't scale, and there's no way to distinguish "upgrade didn't run" from "upgrade ran but agent crashed" without checking both this field and `AgentConnected` together. |
| **Report the full `version.Info` struct (GitCommit, BuildDate, GoVersion, etc.) instead of just `GitVersion`** | Considered; not chosen for the first cut because a single comparable string is enough to answer "did it converge," and it's what `K8sVersionAnnotation` already does elsewhere in this API for the same kind of comparison. Nothing prevents adding more fields later — this is purely additive. Left open (§6, Q1). |
| **Also report `bootID`** (a reboot/process-instance identifier, mirroring `Node.status.nodeInfo.bootID`) | Considered and deliberately left out for now, not rejected outright — genuinely undecided rather than clearly wrong. Left as an open question (§6, Q3) rather than built, to keep this ADR to the two fields there's actual conviction behind. |
| **Rely on `ByoHost`'s name/hostname alone for identity, as today** | This is the status quo `onboarding-adr.md` §4.5 already flags as a risk: hostnames can change, collide at fleet scale, or get reused when a host cycles through the capacity pool, with nothing to detect it. `MachineID` doesn't fix that by itself but makes the underlying fact checkable, which nothing does today. |
| **Build the full controller-driven design instead of this minimal step** | That's `agent-self-upgrade-adr.md`. Not mutually exclusive with this one — this ADR is deliberately the independently shippable subset of it, in case that's as far as this work goes. |

---

## 5. Testing plan

- **Unit/envtest**, extending the existing heartbeat coverage in
  `agent/reconciler/reconciler_test.go`: assert `Status.AgentVersion` is written alongside
  `Status.LastHeartbeatTime` on the same tick. `version.GitVersion`
  (`agent/version/version.go:12`) is a package-level `var`, not an ldflags-only constant, so a
  test can set it directly (`version.GitVersion = "v-test-1"`) without needing a real build —
  no new test infrastructure required.
- **Unit** for the new machine-ID reader, mirroring the existing test for `getOperatingSystem`
  exactly: an injected fake read function returning canned `/etc/machine-id` content, the
  systemd fallback path, and the not-found case — no real filesystem access needed, same as
  today's `getOperatingSystem` tests.
- **e2e**: one small addition, either its own spec or folded into an existing one, that exercises
  both fields against the scenario this ADR is for — an operator manually upgrading a host the
  way they really would:
  1. Stand up a `ByoHostRunner` docker host (as in `cluster_upgrade_test.go:65-103`) running
     agent binary `v-e2e-old`; confirm `ByoHost.Status.AgentVersion == v-e2e-old`,
     `Status.MachineID` is non-empty, and `AgentConnected=True`.
  2. Simulate the manual upgrade an operator would actually perform: copy a second prebuilt
     binary into the running container with the existing `copyToContainer` helper
     (`test/e2e/docker_helper.go:70`), replace the binary the service runs, and restart the
     agent process inside the container — no new e2e harness needed, this is the same
     docker-exec primitive `ByoHostRunner` already uses to start the agent.
  3. Assert `ByoHost.Status.AgentVersion` flips to `v-e2e-new` and `AgentConnected` recovers
     within `HeartbeatTimeoutPeriod` — proving the field and the existing condition together are
     sufficient to validate a manual upgrade, which is the entire point of this ADR.
  4. Assert `Status.MachineID` is **unchanged** across that same restart — the same container,
     same machine, only the agent binary and its version moved. This is what distinguishes the
     two fields' semantics in practice: one is expected to change on upgrade, the other isn't.

---

## 6. Open questions

1. **Should richer build info be reported** (`GitCommit`, `BuildDate`) or is `GitVersion` alone
   sufficient? Leaning toward starting minimal (§4) and adding fields later if a real need shows
   up — e.g. distinguishing two builds that share a version tag.
2. **Is a plain status string enough, or does this want its own condition** (e.g.
   `AgentVersionReported`) the way `AgentConnected` exists for liveness? Leaning toward "no" —
   the field itself is directly comparable to a desired value, which is simpler than a boolean
   condition would be here, but worth revisiting if `agent-self-upgrade-adr.md`
   ends up needing one for its own convergence check.
3. **Should `bootID`** (or an equivalent per-process/per-boot instance identifier) be added
   later?** Deliberately left out for now (§4) — genuinely undecided, not rejected. Would help
   distinguish "same machine, agent restarted" from "same machine, never restarted," which
   `MachineID` alone can't do since it's stable across both. Revisit if that distinction turns
   out to matter once real upgrade activity (manual or via
   `agent-self-upgrade-adr.md`) is happening.
4. **What, if anything, should ever act on a `MachineID` change?** This ADR deliberately stops at
   reporting the fact. Whether a change should raise an alert, block reconciliation, or do
   nothing beyond being visible in `kubectl` is `onboarding-adr.md` §8, Q6's question, not
   answered here.
5. **Where exactly does `/etc/machine-id` come from on a freshly re-provisioned or cloned VM
   image?** Lower risk than it first looks: `machine-id` also feeds DHCP client identification
   (DUID/client-id), so a template that clones it verbatim tends to produce visible DHCP lease
   collisions independent of anything in this ADR — a fleet with working networking has almost
   certainly already hit and fixed this (typically via `systemd-machine-id-setup` regenerating it
   on first boot). Still worth a one-time check against how BYOH hosts are actually provisioned,
   but not a standing worry the way an undetected hostname collision is.

---

## 7. Evidence index

| Claim | Location |
|---|---|
| No agent-version field anywhere in `ByoHost`/`ByoHostStatus` today | `apis/infrastructure/v1beta1/byohost_types.go:67-96` |
| Agent version only ever printed via `--version`, never reported to the API server | `agent/main.go:100,144-148`; `agent/version/version.go:33-46` |
| `GitVersion` and friends are package-level vars, settable directly in tests | `agent/version/version.go:11-18` |
| `ByoHost` name/CN/certificate identity all derive from `os.Hostname()`, flagged as a risk | `onboarding-adr.md` §4.5 |
| Hosts detach and rejoin the capacity pool, so `ByoHost` identity outlives any one cluster attachment | `test/e2e/byohost_reuse_test.go` |
| Existing injectable-reader idiom for host facts, mirrored for `/etc/machine-id` | `agent/registration/host_registrar.go:145-174` (`getHostInfo`, `getOperatingSystem`) |
| Heartbeat write cadence this design reuses (agent side) | `agent/reconciler/host_reconciler.go:41,103-109` |
| `AgentConnected` computed from `LastHeartbeatTime` vs. `HeartbeatTimeoutPeriod` (management side) — the liveness half of validation this ADR pairs with | `controllers/infrastructure/byohost_controller.go:31,94,104-108`; `apis/infrastructure/v1beta1/condition_consts.go:84-96` |
| Existing docker-backed e2e host harness and container-copy helper this plan's e2e spec builds on | `test/e2e/docker_helper.go:48,70,167-336`; `test/e2e/cluster_upgrade_test.go:65-103` |
