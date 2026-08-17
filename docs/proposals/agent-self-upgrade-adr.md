# ADR: Controller-driven, staged self-upgrade of the BYOH host agent

**Status:** Proposed — not yet accepted
**Date:** 2026-08-10 (revised 2026-08-14)
**Deciders:** TBD
**Depends on:** [agent-version-reporting-adr.md](agent-version-reporting-adr.md) — this ADR assumes
`ByoHostStatus.AgentVersion` already exists and only adds fields on top of it.
`Status.AgentVersion` and the heartbeat mechanism it rides alongside are already implemented on
`main`, independent of whether this ADR is accepted (`agent/reconciler/host_reconciler.go:111`).
**Related:** existing agent packaging (the `Makefile`'s `build-host-agent-deb`/`build-host-agent-rpm`
targets, and the process-supervisor unit this repo's reference implementation runs the agent under)
— this design builds directly on that packaging output rather than deferring around it (see the
revision note below).

> §1 describes the current state, checked directly against the code cited inline. §2 onward is
> new design — none of it is built yet. Open questions are listed in §8, not hidden inside the
> decision.
>
> Kubernetes-version upgrades (kubelet/control-plane) are a separate, already-implemented
> mechanism and are explicitly **not** in scope.
>
> **Revision note (2026-08-14):** the original draft of this ADR deferred the upgrade's deployment
> mechanism entirely — an "opaque script," content unspecified — to avoid coupling to an in-flight
> packaging decision. Design review concluded that deferral bought flexibility nobody actually
> needed, at the cost of a real security surface (arbitrary root-privileged script content run
> continuously, not once) and extra machinery (per-host Secret creation and templating). This
> revision commits to one narrow mechanism instead — pull an OCI-packaged `.deb`/`.rpm` via
> `imgpkg` (the same mechanism already used elsewhere in this repo, not a new one) and hand it to
> the host's own package manager — and removes the script/Secret path entirely. See §2.2 and §4
> for the reasoning and the alternative it replaces.

---

## 1. Context

### 1.1 There is currently no way to upgrade the agent binary on an already-registered host

- The agent reports its own build version only via a CLI flag: `agent/main.go` parses `--version`
  and prints `version.Get()` (`agent/version/version.go`, ldflags-populated by `hack/version.sh`
  at build time) to stdout, then exits. `Status.AgentVersion`
  ([agent-version-reporting-adr.md](agent-version-reporting-adr.md)) now also reports it to the
  management cluster every heartbeat tick (`agent/reconciler/host_reconciler.go:111`) — that half
  of the gap is closed; this ADR is about the other half, driving and staging the upgrade itself.
- `ByoHostSpec` (`apis/infrastructure/v1beta1/byohost_types.go:37-53`) has only `BootstrapSecret`,
  `InstallationSecret`, `UninstallationSecret` — nothing that lets the management cluster tell a
  host to change its agent version.
- No controller anywhere compares a desired vs. observed agent version.
- The repo already produces the agent as **both** package formats — `build-host-agent-deb` and
  `build-host-agent-rpm` (`Makefile:428-480`), with separate `debsrc`/`rpmbuild` source trees. Any
  upgrade mechanism that only handles one of the two silently strands hosts running the other.
- `ByoHost.Status.HostDetails` (`HostInfo`, `apis/infrastructure/v1beta1/byohost_types.go:55-64`)
  already carries `OSName`, `OSImage`, and `Architecture` — the last set by the agent itself via
  `runtime.GOARCH` at registration (`agent/registration/host_registrar.go:149`). These live only
  in `Status` today, not on `.Labels`, so they are visible (e.g. via the existing `Arch`
  `+kubebuilder:printcolumn`, `byohost_types.go:121`) but not currently selectable by a
  `metav1.LabelSelector` — relevant to §2.1.
- No e2e test exercises an agent-binary upgrade. `test/e2e/cluster_upgrade_test.go` and
  `clusterclass_upgrade_test.go` are Kubernetes-version upgrade tests: they run one fixed agent
  binary for the whole test and validate success purely via `framework.WaitForNodesReady` against
  the target `KubernetesVersion`. That is a different axis entirely and this ADR does not touch
  it.

**Net effect:** there is no mechanism today to *drive* an agent upgrade across a fleet from the
management cluster — staged, observable, or otherwise. An operator can already upgrade a host by
hand (SSH, Ansible), and can now *validate* that with
[agent-version-reporting-adr.md](agent-version-reporting-adr.md) alone; what's still missing is
staging that across many hosts with a blast-radius limit, from inside the cluster rather than from
Ansible's own batching.

### 1.2 What the design can build on

**The heartbeat loop and `AgentConnected` already give a validated, continuously-updated liveness
signal.** The agent writes `Status.LastHeartbeatTime` from its own reconcile loop no more often
than `HeartbeatInterval` (`agent/reconciler/host_reconciler.go:41,103-109`). The management-cluster
`ByoHostReconciler` compares that timestamp against `HeartbeatTimeoutPeriod`
(`controllers/infrastructure/byohost_controller.go:31,94,103-108`) and sets the `AgentConnected`
condition (`apis/infrastructure/v1beta1/condition_consts.go:84-96`) accordingly. Between that and
`Status.AgentVersion`, a rollout controller has everything it needs — no new liveness plumbing.

**The agent already runs under a process supervisor that restarts on crash, with a hard limit, not
backoff — but that limit only works if the interval is wider than the restart delay, which this
repo's config didn't originally get right.** systemd's `Restart=always` restarts unconditionally;
`StartLimitBurst`/`StartLimitIntervalSec` is meant to give up (`start-limit-hit`) after too many
restarts too fast. But `StartLimitBurst` only counts restarts that land inside a single
`StartLimitIntervalSec`-wide window, and `Restart=` never fires faster than `RestartSec` apart —
so if `RestartSec >= StartLimitIntervalSec`, consecutive restarts can never land in the same
window and the limit **cannot trip, at any `StartLimitBurst` value.** This repo's original
`RestartSec=5s` / `StartLimitIntervalSec=5s` pairing had exactly that bug (empirically confirmed
in a privileged systemd container against a fake always-failing binary: 6 restarts over 30s, never
hit `start-limit-hit`, would have retried forever) — meaning a genuinely broken binary never
actually stopped retrying, contrary to what an earlier draft of this ADR assumed. Current values,
also empirically validated the same way (limit correctly hit at t+27s after 5 restarts):
`RestartSec=5s`, `StartLimitIntervalSec=60s`, `StartLimitBurst=5`
(`service/pf9-byohostagent.service`). Treat the exact numbers as a deployment tunable this ADR
doesn't depend on, but **do not shrink `StartLimitIntervalSec` back toward `RestartSec`** — that
reintroduces the bug regardless of what `StartLimitBurst` is set to. The qualitative property that
matters: **no indefinite retry, and no exponential backoff**, so recovery from a truly broken
binary is manual (SSH/Ansible) by design, matching this ADR's decision not to build automatic
rollback (§2.4). One detail that matters for §2.2 step 5, true of systemd's `Restart=` accounting
generally: `StartLimitBurst` counts every supervisor-triggered relaunch, regardless of exit code —
a clean, successful, intentional exit consumes the same budget as a crash.

**Revision (post-implementation review):** §2.2 step 5 originally used `syscall.Exec` specifically
to avoid spending one of that budget's slots on the deliberate switch to a new binary. Reviewed
after Phase 3 landed and the complexity was visible end to end, that trade wasn't worth it — see
§2.2 step 5 and §4 for the full reasoning. The budget concern (a deliberate `os.Exit(0)` consuming
the same slot a real crash would) is still real and still addressed by giving the unit headroom —
it's just folded into the `StartLimitBurst=5` figure above rather than being a separate, smaller
bump, since fixing the interval bug had to happen first for the burst count to mean anything at
all.

**Kubelet is already fully independent of the agent's process lifecycle, unconditionally** —
relevant because it means the choice between exiting and re-exec'ing never actually turned on
kubelet safety, even though that was the original justification for the more complex option.
`kubelet` is installed as its own package (`for pkg in cri-tools kubernetes-cni kubectl kubelet
kubeadm`, `installer/internal/algo/ubuntu-templates/install.sh.tmpl:50`) and ends up running under
its own systemd unit; the agent's own `CmdRunner` only ever shells out to run the install script
to completion and exits (`agent/cloudinit/cmd_runner.go:24`) — it never parents or supervises
kubelet at runtime. An agent process restarting doesn't disrupt kubelet either way.

**Pulling an OCI-packaged artifact and installing it with the host's own package manager already
has precedent in this repo, twice over.** `downloadDebianPackage` / `installDebianPackage`
(`cmd/byohctl/service/agent.go:192-240`) run `imgpkg pull -i <ref> -o <dir>` then `dpkg -i` on the
extracted `.deb` — during initial onboarding only, today. Separately, the k8s-component installer
does the exact same `imgpkg pull` for a different artifact
(`installer/internal/algo/ubuntu-templates/install.sh.tmpl:29`), and that same script also
self-installs `imgpkg` from GitHub releases if it isn't already on `PATH`
(`install.sh.tmpl:9-24`) — meaning `imgpkg` is already expected to be present on any host that has
completed initial k8s-component installation, which every host eligible for an agent upgrade
necessarily has. §2.2 reuses this exact mechanism rather than a plain HTTP(S) fetch: `imgpkg pull`,
then hand the extracted `.deb`/`.rpm` to the host's own package manager. If `imgpkg` is somehow
missing on an eligible host, that's a legitimate `AgentUpgradeSucceeded=False` — this design does
not reimplement `install.sh.tmpl`'s self-install fallback for it.

---

## 2. Decision

When the management cluster wants a host on a different agent version, it tells the host exactly
which package to install — an OCI image reference, pulled via `imgpkg`, resolved by whoever
creates the upgrade CR, not by the agent or controller — and the agent's only job is to pull it,
extract the `.deb`/`.rpm` matching its own package family, hand it to `dpkg`/`rpm`, and on success
exit so the process supervisor relaunches it running the new binary. A new **rollout controller**
stages this across a
fleet, gating on the existing heartbeat/`AgentConnected` signal and the new `AgentVersion` field,
and halting hard on any explicit failure while tolerating pre-existing or transient unavailability
by simply pausing rather than failing.

### 2.1 API changes

On top of `ByoHostStatus.AgentVersion`
([agent-version-reporting-adr.md](agent-version-reporting-adr.md)), `ByoHostSpec` gains:

```go
// DesiredAgent, when set, drives a managed agent upgrade on this host. The
// three fields below are always set together — see DesiredAgentSpec — so
// nil unambiguously means "no managed upgrade in progress," rather than
// relying on convention across three independently-optional strings.
// +optional
DesiredAgent *DesiredAgentSpec `json:"desiredAgent,omitempty"`

// DesiredAgentSpec is the agent version/package a host should converge on.
type DesiredAgentSpec struct {
	// Version is the agent version this host should be running. The agent
	// compares this against its own version.Get().GitVersion on every
	// reconcile tick (cheap — no fetch) and only acts on a mismatch.
	Version string `json:"version"`

	// PackageURL is an OCI image reference — pulled via `imgpkg pull`, the
	// same mechanism already used for k8s-component bundles and the byohctl
	// onboarding .deb pull — for a bundle containing the .deb/.rpm that
	// installs Version. Not templated and does not point at a Secret — an
	// image reference is not sensitive, unlike the opaque-script content an
	// earlier draft of this ADR used. The agent extracts the bundle, picks
	// the single *.deb or *.rpm inside it matching its own already-known
	// package manager, and runs `dpkg -i`/`rpm -Uvh`; nothing else is
	// executed. Pinning this reference by digest (`@sha256:...`) rather
	// than a mutable tag is the primary integrity mechanism — see
	// PackageChecksum for a secondary, narrower check.
	// +optional
	PackageURL string `json:"packageURL,omitempty"`

	// PackageChecksum, if set, is the expected checksum ("sha256:<hex>") of
	// the specific .deb/.rpm file extracted from the PackageURL bundle —
	// not of the OCI image itself, which digest-pinning the reference above
	// already covers. The agent refuses to install on mismatch and marks
	// AgentUpgradeSucceeded=False with reason PackageChecksumMismatch.
	// +optional
	PackageChecksum string `json:"packageChecksum,omitempty"`
}
```

`DesiredAgentSpec` is copied down as a unit from `ByoHostAgentUpgradeSpec` onto each targeted
`ByoHost` by the rollout controller, rather than having the agent read the `ByoHostAgentUpgrade` CR
directly — this isn't incidental duplication, it's the same shape `InstallationSecret`/
`BootstrapSecret` already use. The agent only ever reads/updates its own `ByoHost` object; it has
no way to know which `ByoHostAgentUpgrade` (if any) targets it without List/Watch access to a CRD
it currently has no business touching, and re-evaluating `Selector` matches on the host side would
duplicate logic the controller already owns and risks two different answers if it ever disagreed
with itself. Selector evaluation stays the controller's job alone, for every host, all the time.
`PackageChecksum` follows for the identical reason, not a separate one: since the agent can't reach
the CR at all, anything it needs to act on has to be pushed down alongside the URL — there's no
cheaper alternative once the URL is already there. Grouping the three fields under one struct
(rather than three top-level, independently-optional strings) was a deliberate refinement over the
original sketch: the controller only ever sets or reads them as a unit, so the type now enforces
that invariant instead of relying on convention.

Two new labels, mirroring the existing `AttachedByoMachineLabel` domain and pattern
(`byoh.infrastructure.cluster.x-k8s.io/...`, `apis/infrastructure/v1beta1/byohost_types.go:21`,
applied by controller code the same way `clusterv1.ClusterNameLabel` already is,
`controllers/infrastructure/byomachine_controller.go:588`):

```go
// HostArchitectureLabel mirrors Status.HostDetails.Architecture onto
// .Labels so it becomes selectable — Kubernetes label selectors cannot
// match on arbitrary status fields.
HostArchitectureLabel = "byoh.infrastructure.cluster.x-k8s.io/architecture"

// HostOSFamilyLabel mirrors a coarse package-family derived from
// Status.HostDetails.OSName/OSImage ("debian" or "rhel"), for the same
// reason.
HostOSFamilyLabel = "byoh.infrastructure.cluster.x-k8s.io/os-family"
```

These are set once, at registration, alongside the existing `HostInfo` write
(`agent/registration/host_registrar.go:149`) — the agent already knows both values about itself at
that point, so this is a label added to an object the agent is already creating, not a new
control-flow path or a new management-side controller.

**Why labels instead of automatic per-host resolution:** an earlier iteration of this design had
the rollout controller resolve the correct package URL per host automatically (mirroring how
`installer/registry.go`'s `AddOsFilter` resolves OS-specific k8s-component bundles,
`registry.go:141-147`). That was dropped in favor of manual cohort-splitting via `Selector`: there
is no existing version→package-URL naming convention for the agent to build a resolver on top of,
and a fleet is expected to be genuinely mixed (deb/rpm, amd64/arm64) with no single
`ByoHostAgentUpgrade` needing to span more than one cohort. An operator (or UI) creates one CR per
cohort, each with its own explicit `PackageURL` — see §8 Q9 for the gap this trades away.

`ByoHostAgentUpgradeSpec.TargetVersion` (§2.3) must be an explicit, fully-resolved version string —
never `latest` or any other floating reference — enforced at the API level, not by convention:

```go
// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+(\.[0-9]+)?(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`
TargetVersion string `json:"targetVersion"`
```

matching the same strict-semver shape `hack/version.sh:55` already enforces at build time (and
rejecting `latest` as a side effect, since it doesn't match).

### 2.2 Agent-side: pull, install, exit

New function on the existing `HostReconciler`, `executeAgentUpgrade`, invoked from the same
reconcile tick that already writes the heartbeat:

1. Compare `ByoHost.Spec.DesiredAgent.Version` to `version.Get().GitVersion`. Equal, or
   `DesiredAgent` nil → no-op. Pure function, no fakes needed to test.
2. On mismatch, require `DesiredAgent.PackageURL` — if unset, wait (same pattern as
   `executeInstallerController` waiting on `InstallationSecret`,
   `agent/reconciler/host_reconciler.go:142-146`).
3. `imgpkg pull -i <DesiredAgent.PackageURL> -o <tempDir>`, then find the single `*.deb` (package
   family `debian`) or `*.rpm` (package family `rhel`) in `tempDir` — the agent already knows
   which via `registration.GetOSFamily`, the same probe used to set `HostOSFamilyLabel` at
   registration (§2.1). Zero or more than one match is a hard error (`AgentUpgradeSucceeded=False`,
   reason `PackageBundleInvalid`) — not something to guess at. If `DesiredAgent.PackageChecksum` is
   set, verify it against that extracted file before doing anything else — mismatch marks
   `AgentUpgradeSucceeded=False`, reason `PackageChecksumMismatch`, and stops here without
   installing.
4. Hand the extracted file to the host's own package manager — a **fixed, two-way branch based on the
   agent's own already-known package format**, never an operator-supplied command:
   `dpkg -i <path>` or `rpm -Uvh <path>` — no `--oldpackage`/force-downgrade flag, ever (see §2.4:
   a downgrade failing here is the intended backstop, not a bug to route around).
   `CmdRunner.RunCmd` runs it, same interface
   `executeInstallerController` already uses (`agent/reconciler/host_reconciler.go:206`), but with
   no template parsing involved at all — the command is constructed by the agent, not supplied as
   content from outside. This is deliberately narrower than the original opaque-script design
   (§4): no arbitrary root-privileged script content, ever.
5. On success: `os.Exit(0)` and let the process supervisor's `Restart=always` relaunch the
   service. **Revision (post-implementation review):** the original design here was
   `syscall.Exec` into the freshly installed binary — same PID, in-place, no service restart —
   specifically to avoid spending one of the supervisor's `StartLimitBurst` slots (§1.2) on the
   deliberate switch itself. That benefit was real but narrow (it only changes how many *further*
   automatic retries a freshly-installed binary gets before `start-limit-hit` demands a human —
   one fewer, without it), and came at a real cost once actually built: a new `IExecer` interface
   and fake (~150 lines), and an `os.Executable()`/`os.Args`/`os.Environ()`-preservation assumption
   that no test ever validated against a real re-exec — every test used a fake `IExecer`, so
   whether the re-exec'd process genuinely comes back up with the right flags was unverified.
   Reviewed after Phase 3 landed and the actual shape of the complexity was visible: the same
   headroom is available for a one-line config change — §1.2's revision note bumps
   `StartLimitBurst` by exactly the amount the deliberate switch would otherwise consume — with no
   new Go code, no unvalidated assumption, and one restart code path (systemd's) instead of two.
   See §4 for the alternative this replaces.
6. On `dpkg`/`rpm` failure: don't exit. Mark `AgentUpgradeSucceeded=False`, reason
   `PackageInstallFailed`, emit an event — the identical pattern `executeInstallerController`
   already uses for `InstallScriptExecutionFailed`. Leave `Spec.DesiredAgent` untouched; retrying
   with a fixed URL is the rollout controller's job (§2.3), not the agent's. In practice this is
   the failure signal that fires *fastest* and most
   often — `dpkg`/`rpm` exit non-zero on most real breakage (bad package, unmet dependencies,
   failed postinst), unlike an arbitrary script which could silently exit 0 having done nothing.
7. The new process, once systemd relaunches it, starts normally and its next heartbeat tick
   writes `Status.AgentVersion = version.Get().GitVersion` alongside `LastHeartbeatTime`. **This is
   also how a silently-ineffective install gets caught**: if the install "succeeded" but the
   binary on disk didn't actually change, the relaunched process is still the old binary,
   `Status.AgentVersion` never moves, and the rollout controller's convergence check (§2.3) treats
   it identically to any other stuck host.
8. If the new binary crashes immediately after relaunch, there is nothing left for *this* agent
   process to report — it already exited cleanly in step 5, on its own terms, before the crash
   happened in the *next* process. Recovery is whatever the systemd unit does (§1.2: up to 5
   restarts within a 60s window, one already spent on the deliberate switch, then it stops trying —
   roughly 20-30s of real wall-clock time for a fast, tight crash loop) — this is caught by the
   rollout controller's availability accounting (§2.3) via `AgentConnected`, not by an agent-side
   signal, and is expected to require manual (SSH/Ansible) recovery, consistent with §2.4.

### 2.3 Management-side: a rollout controller

A new namespaced CRD, `ByoHostAgentUpgrade`, and controller `byohostagentupgrade_controller.go`,
modeled on the `maxUnavailable` rolling-update pattern Cluster API's own `MachineDeployment`
already uses:

```go
type ByoHostAgentUpgradeSpec struct {
    Selector         metav1.LabelSelector // which ByoHosts this rollout targets, within this CR's own namespace
    TargetVersion    string               // strict semver only, see §2.1
    PackageURL       string               // OCI image ref (imgpkg-pulled) for this cohort's .deb/.rpm bundle
    PackageChecksum  string               // optional, "sha256:<hex>"
    MaxUnavailable   *intstr.IntOrString  // default 1
    PerHostTimeout   metav1.Duration      // default 10m
}

type ByoHostAgentUpgradeStatus struct {
    Phase             string // Pending | Progressing | Completed | Failed
    Total             int32
    Upgraded          int32
    UnavailableCount  int32  // the live union count below, exposed for observability/tests
    FailedHosts       []string
    Conditions        clusterv1.Conditions
}
```

**Scope is namespace, not a new concept.** `ByoHost`/`ByoCluster` are already `Namespaced`-scope
CRDs (`byohost_types.go:117`, `byocluster_types.go:53`). A `ByoHostAgentUpgrade` created in a given
namespace only ever selects `ByoHost`s in that same namespace — namespace boundaries already
provide whatever isolation an operator uses namespaces for, with zero new API surface. `Selector`
handles any further sub-scoping within that namespace (e.g. one `ByoCluster`'s hosts via
`clusterv1.ClusterNameLabel`, or one OS/arch cohort via the labels added in §2.1).

**Availability accounting.** Define, over the set of `ByoHost`s matching `Selector` (within this
CR's namespace):

```
InFlight     = { h : h.Spec.DesiredAgent != nil AND h.Spec.DesiredAgent.Version == TargetVersion
                     AND h.Status.AgentVersion != TargetVersion
                     AND AgentUpgradeSucceeded(h) != False }

Disconnected = { h : AgentConnected(h) != True }

Unavailable  = InFlight ∪ Disconnected     // union — a host in both sets counts once
```

`InFlight` is scoped to hosts *this rollout has already assigned* `DesiredAgent.Version` to — not
every not-yet-touched host in the selected fleet (which would trivially include the whole cohort
at the start of any rollout and permanently block step 3 below). `Disconnected`, in contrast, is
computed over the **entire** selected cohort, touched or not — this is what gives continuous
protection against a host that already converged and later crashed, without a bake/soak duration
(see §4 for why a duration was considered and rejected).

Reconcile loop, once per tick:

1. List `ByoHost`s matching `Selector` in this CR's namespace. Compute `Unavailable` as above;
   write `Status.UnavailableCount = |Unavailable|`.
2. If `|Unavailable| < MaxUnavailable`, pick `MaxUnavailable - |Unavailable|` more not-yet-upgraded
   hosts, set `Spec.DesiredAgent = &DesiredAgentSpec{Version: TargetVersion, PackageURL:
   PackageURL, PackageChecksum: PackageChecksum}` directly — no Secret is created, unlike the
   original opaque-script draft (§4).
3. For each `InFlight` host, check convergence: `Status.AgentVersion == TargetVersion` AND
   `AgentConnected == True`, evaluated as current state, not a required transition (an earlier
   draft required `AgentConnected` to have gone `False→True`, which doesn't hold for a healthy
   exit-and-relaunch cycle short enough to never miss a heartbeat — dropped as incorrect).
   Converged → increment `Upgraded`, this host leaves `InFlight`.
   `AgentUpgradeSucceeded == False`, or `PerHostTimeout` elapsed with neither → failed (see step 5).
4. **Two distinct "can't proceed" states, not one:**
   - **Blocked (soft, self-healing):** `|Unavailable| >= MaxUnavailable` with no
     `AgentUpgradeSucceeded == False` anywhere in `InFlight`. `Phase` stays `Pending` (if nothing
     has been picked yet) or `Progressing` (if some hosts already converged). No new hosts are
     picked, but nothing is marked failed either — if a `Disconnected` host recovers (including one
     entirely unrelated to this rollout), the budget frees up and picking resumes automatically
     next tick. Surface the reason in a condition/event (e.g. `Reason:
     InsufficientAvailabilityBudget`, `Message: "N of M selected hosts already unavailable;
     MaxUnavailable is K"`) so an operator doesn't mistake a starved rollout — which can happen
     entirely from pre-existing, unrelated unavailability in the selected cohort — for a stuck
     controller.
   - **Failed (hard, terminal, needs an operator):** any `InFlight` host reaches
     `AgentUpgradeSucceeded == False` (explicit `dpkg`/`rpm` failure or checksum mismatch) or
     `PerHostTimeout` (silent failure — install "succeeded" but nothing ever converged, or the new
     binary never comes back). `Phase = Failed`, that host recorded in `FailedHosts`, and — the
     deliberate choice from the original draft, unchanged — **no further hosts are ever selected
     by this CR again.** Already-upgraded hosts are left as-is; this design does not attempt
     automatic rollback, fleet-wide or per-host (§2.4).
5. All `Selector`-matched hosts converged, none failed → `Phase = Completed`.

### 2.4 Rollback

**Not performed through this mechanism at all — always a manual, out-of-band operator action
(SSH/Ansible), never a CR.** This was an open question in the original draft (previously §8 Q4);
resolved, and resolved more strictly than "no automatic rollback": there is no supported path for
`ByoHostAgentUpgrade`/the agent's fixed install step to move a host to an *older* version, full
stop. `rpm -Uvh` (§2.2 step 4) is deliberately never given `--oldpackage` or any equivalent
force-downgrade flag — not conditionally omitted, never added, by decision. A `ByoHostAgentUpgrade`
whose `TargetVersion` happens to be older than a selected host's current version simply fails that
host's install step every time, the same as any other bad package would. That's the intended
backstop, not a gap: **downgrading a host that runs continuously as root is judged to always
warrant a human looking at it directly**, not a CR someone could create (however deliberately)
without necessarily understanding what a downgrade does to that specific version pair. If a fleet
needs to move backward, that happens the same way recovering a host stuck after a bad upgrade
already does (§1.2) — SSH/Ansible, entirely outside this design — not by pointing this mechanism
at an old version and expecting it to work.

---

## 3. Consequences

**Positive.** A blast-radius-limited, observable way to converge agent versions across a fleet
without touching packaging or requiring re-onboarding. The agent-side change reuses an existing,
already-tested execution pattern (`executeInstallerController`) with *less* new surface than the
original draft, not more: no template parsing, no per-host Secret, no arbitrary script content —
just a fetch and a fixed two-command branch. `Unavailable`'s union accounting gives continuous
post-convergence protection (a host that crashes after converging blocks the *next* batch) without
a bake/soak timer, using a signal (`AgentConnected`) that already exists.

**Negative / cost.** A new controller, CRD, and two new host labels to maintain. Committing to
`dpkg`/`rpm` specifically (rather than an opaque, deployment-agnostic script) means this ADR now
*is* coupled to package-manager-based deployment — if a future packaging format ever replaces
`.deb`/`.rpm` for this agent, this mechanism needs revisiting rather than being deployment-agnostic
by construction. That coupling was accepted deliberately: it's a small, enumerable, already-in-use
set (Makefile already builds both formats today) versus arbitrary script content with no
provenance story (§8 Q3). Manual cohort-splitting via `Selector` + explicit `PackageURL` per CR
means an operator (or their UI) is responsible for pointing each cohort at the right artifact —
nothing in this design catches an accidental deb-URL-to-an-rpm-host mismatch before it fails at
install time (§8 Q9).

**Risk if not done.** Fleet-wide rollouts stay dependent on whatever staging and failure-handling
external tooling (Ansible or otherwise) provides, with no in-cluster visibility beyond what
[agent-version-reporting-adr.md](agent-version-reporting-adr.md) gives per-host.

---

## 4. Alternatives considered

| Alternative | Why rejected (or: why it might actually be the right call) |
|---|---|
| **Manual upgrades via Ansible/SSH, validated by [agent-version-reporting-adr.md](agent-version-reporting-adr.md) alone, and nothing more** | Not clearly wrong — Ansible already has `serial`/`max_fail_percentage` for staged rollout and halt-on-failure, the two properties this ADR's controller exists to provide. The case for building this anyway is teams that want the safety net enforced from the cluster side rather than trusted to a script someone might run with `serial: 0` by mistake, or that want in-cluster visibility without depending on Ansible's own reporting. Shipping only the version-reporting ADR and stopping there remains a legitimate outcome. |
| **Opaque upgrade script, content unspecified (the original draft of this ADR)** | Rejected on revision: deferring the deployment mechanism bought flexibility nobody needed, at the cost of a real security surface (arbitrary root-privileged script, run continuously, not once — a larger blast radius than the already-existing `InstallationSecret`, which at least only runs once during bootstrap) and extra machinery (per-host Secret creation and templating on the controller side). The fleet has exactly two real package formats today (§1.1); naming them directly is simpler and safer than staying agnostic to a set of one-or-two known things. |
| **Automatic per-host resolution of the correct package URL by host OS/arch** (mirroring `installer/registry.go`'s OS-filtered bundle resolution) | Considered, dropped in favor of manual `Selector`-based cohort splitting (§2.1). There's no existing version→package-URL naming convention for the agent to resolve against, unlike k8s components which already have one; building that convention now would be new machinery nobody asked for, for a benefit (avoiding manual cohort-splitting) that a `Selector` already delivers with zero new code. |
| **Minimum bake/soak duration after convergence, before a host is considered "done" and its slot freed** | Considered, rejected. Any fixed duration still has an escape case — a delayed crash after the window elapses — so it only shifts the threshold at which delayed failures slip past the blast-radius limit, it doesn't remove the risk. The `Unavailable` union (§2.3) already gives continuous protection against *fast* post-convergence regressions using an existing signal and no new duration field or state tracking; slow-onset regressions past that point are accepted residual risk, same as any staged rollout (including vanilla Kubernetes `minReadySeconds`). |
| **Automatic rollback**, fleet-wide or per-host, on failure | Rejected (§2.4). Halting and waiting for an explicit operator action is safer than any automatic corrective action touching more hosts on the controller's own initiative, and a broken host already needs manual recovery regardless of whether rollback is automatic (§1.2's `StartLimitBurst` behavior). |
| **`syscall.Exec` into the freshly installed binary** (this ADR's original §2.2 step 5, in place through Phase 2/3), instead of `os.Exit(0)` + process-supervisor restart | **Reversed on post-implementation review** — this is now the rejected alternative, not the decision. The original justification, "a restart would disrupt kubelet," was wrong from the start: kubelet runs under its own supervision, never parented by the agent, so it's unaffected either way (§1.2). The real benefit — never spending a `StartLimitBurst` slot on the deliberate switch — was real but narrow (one fewer automatic crash-retry for the new binary without it), and cost a new `IExecer` interface plus fake (~150 lines) and an `os.Executable()`/argv/env-preservation assumption no test ever exercised against a real re-exec. Once Phase 3 was built and the complexity was visible, the team judged the trade not worth it: the same budget headroom is available for a one-line `StartLimitBurst` bump (§1.2), with no new code and no unvalidated assumption. |
| **DaemonSet-style rolling update** | BYOH hosts are not Kubernetes-managed compute the way a DaemonSet's nodes are — the agent is what makes a host *become* part of the cluster. |
| **Imperative per-host Task objects instead of desired-state fields** | Every other mechanism in this repo (`K8sVersionAnnotation`, `InstallationSecret`) is declarative desired-state reconciliation; matching that shape keeps this code looking like the rest of the codebase. |

---

## 5. Testing plan

### 5.1 Unit / envtest coverage (agent side)

Extends `agent/reconciler/reconciler_test.go`'s existing suite for `executeInstallerController`,
using `cloudinitfakes` for `ICmdRunner`, plus plain `PackagePull func(...) error` and `Exit func()`
fields on `HostReconciler` (see §2.2 revision note — no interface/fake needed for a single-method
wrapper with one production implementation).

- Version comparison is a pure function: equal, `DesiredAgent` nil, mismatched — no fakes needed.
- Mismatch with `DesiredAgent.PackageURL` unset → waits, does not error.
- Checksum set and mismatched → `AgentUpgradeSucceeded=False`, reason `PackageChecksumMismatch`;
  install never attempted; `Exit` never invoked.
- `dpkg`/`rpm` selection is a pure function of the agent's own detected package manager — test
  both branches directly, no host-provided input involved.
- Fetch+install succeeds → `Exit` invoked exactly once.
- `dpkg`/`rpm` returns non-zero → `Exit` **not** invoked, `AgentUpgradeSucceeded=False` set with
  reason `PackageInstallFailed`, event fires, `Spec.DesiredAgent` left untouched (so a retry with a
  corrected URL doesn't need anything else to change).

### 5.2 Controller (envtest) coverage — `byohostagentupgrade_controller_test.go`

This is where most of the edge-case surface lives — the `Unavailable` union accounting needs
direct coverage, not just the happy path:

- Batch sizing never exceeds `MaxUnavailable` in-flight hosts regardless of total hosts matching
  `Selector`.
- `InFlight`-only unavailability (no unrelated disconnections): count equals in-flight hosts,
  batch sizing gates correctly.
- `Disconnected`-only unavailability, entirely unrelated to this rollout (a host down before the
  CR was even created): counts toward the budget; with `MaxUnavailable` already consumed, **zero**
  hosts get picked — `Phase` stays `Pending`, condition/event explains why (not silently stuck).
- Overlap: a host that is both `InFlight` and `Disconnected` (the common case right after the
  exit-and-relaunch cycle, briefly) counts **once**, not twice — union, not sum.
- A host converges (`AgentVersion == TargetVersion`, `AgentConnected == True`), then later goes
  `AgentConnected == False` with no `AgentUpgradeSucceeded=False`: `Upgraded` was already
  incremented and does not decrement, but it re-enters `Disconnected`, raising `UnavailableCount`
  and blocking the *next* batch from starting — without ever being marked failed itself. This is
  the specific case the union accounting exists to catch instead of a bake timer.
- `PerHostTimeout` transitions a silently-stuck host to failed and halts the rollout — selecting
  no further hosts even though others haven't been touched yet. `Phase = Failed` is terminal: a
  later tick must not resume picking even if `Disconnected` later drops back below
  `MaxUnavailable`.
- `Blocked` (soft) vs. `Failed` (hard) are distinct and don't get confused: a rollout blocked
  purely by pre-existing/unrelated disconnection self-resolves and resumes picking once the budget
  frees up; a rollout halted by an `AgentUpgradeSucceeded=False` failure never resumes on its own.
- `Phase` transitions (`Pending → Progressing → Completed`, and the `→ Failed` branch) exercised
  directly via `ByoHost.Status` manipulation, no real agents needed.
- `TargetVersion` admission: `latest` and other non-strict-semver values are rejected by the CRD's
  validation pattern before the controller ever sees them.

### 5.3 e2e: `test/e2e/agent_upgrade_test.go`

Follows the existing `ByoHostRunner` / docker capacity-pool pattern
(`test/e2e/cluster_upgrade_test.go:65-103`). Fixture packages replace the fixture scripts from the
original draft — a trivially small `.deb` (and, if the fleet mix is real rather than legacy, a
`.rpm`) that installs a second prebuilt test binary and exits 0/non-zero as needed.

**Spec: "Should stage an agent upgrade across the fleet respecting maxUnavailable."**
- Build two agent binaries with different `-X agent/version.GitVersion=...` values, stand up a
  4-host capacity pool running the old one, confirm all reach `AgentConnected=True`, attach at
  least one to a real `ByoMachine`.
- Create a `ByoHostAgentUpgrade` with a fixture package, `TargetVersion` set to the new version,
  `MaxUnavailable: 1`.
- **Poll continuously during the rollout**: `UnavailableCount` never exceeds `MaxUnavailable` at
  any sampled instant, and the workload-cluster Node stays `Ready=True` throughout (the empirical
  check on kubelet's independence from the agent's own process lifecycle, §1.2).
- After completion: all 4 `ByoHost.Status.AgentVersion` at the new version;
  `Phase == Completed`, `Upgraded == 4`, `FailedHosts` empty; each host's docker container ID is
  unchanged from before the rollout — proof the upgrade was an in-container service restart, not a
  container recreation. (The agent process itself *does* restart — `os.Exit(0)` plus systemd
  `Restart=always`, §2.2 step 5 — this assertion is about the container boundary, not the process.)

**Spec: "Should halt on explicit failure without affecting unaffected hosts."**
- A fixture package whose postinst deliberately exits non-zero.
- Assert exactly one host shows `AgentUpgradeSucceeded=False, Reason=PackageInstallFailed`, its
  `AgentVersion` never changes, `Phase == Failed`, and the other 3 hosts' `DesiredAgent`
  stays nil — matching hard-halt behavior.

**Spec: "Should pause, not fail, on pre-existing unrelated unavailability, and resume once it clears."**
- Stand up the same 4-host pool; before creating the CR, kill (not upgrade-related) one host's
  agent process so it goes `AgentConnected=False`.
- Create a `ByoHostAgentUpgrade` with `MaxUnavailable: 1` targeting all 4.
- Assert zero hosts get `DesiredAgent` set while the pre-existing host stays disconnected,
  `Phase` stays `Pending` with the `InsufficientAvailabilityBudget` condition — not `Failed`.
- Restart the killed host's agent; assert the rollout resumes picking hosts on its own within the
  next few reconcile ticks, with no operator action beyond fixing the unrelated host.

---

## 6. Rough sequencing

- **Phase 0 (prerequisite, separate ADR, already done).**
  [agent-version-reporting-adr.md](agent-version-reporting-adr.md) — `Status.AgentVersion` exists.
- **Phase 1.** §2.1 API additions (`DesiredAgentSpec` with `Version`, `PackageURL`,
  `PackageChecksum`, the two host labels) plus `make generate` and `make manifests`.
  Purely additive, safe to land alone. Mirroring the labels at registration time
  (`agent/registration/host_registrar.go`) is a small, separate, low-risk change.
- **Phase 2.** §2.2 agent-side fetch/install/exit, with unit coverage (§5.1). Testable in
  isolation with fakes, before the controller exists.
- **Phase 3.** §2.3 `ByoHostAgentUpgrade` CRD and controller, including the `Unavailable` union
  accounting, with envtest coverage (§5.2) — this is the highest-edge-case-density piece.
- **Phase 4.** §5.3 e2e specs, using small fixture packages rather than a production build.
- **Phase 5 (follow-up, not blocking).** §8 open questions — package-URL provenance/signing
  beyond a plain checksum, who/what produces the right URL for a given `TargetVersion` at scale,
  and the Selector-mismatch gap (§8 Q9).

---

## 7. Evidence index

| Claim | Location |
|---|---|
| No spec-side field to tell a host to upgrade today | `apis/infrastructure/v1beta1/byohost_types.go:37-53` |
| Agent builds both `.deb` and `.rpm` today | `Makefile:428-480` |
| `HostInfo` (`OSName`/`OSImage`/`Architecture`) lives only in `Status`, not `.Labels` | `apis/infrastructure/v1beta1/byohost_types.go:55-64,121`; `agent/registration/host_registrar.go:149` |
| Existing label-mirroring pattern this design's new labels follow | `apis/infrastructure/v1beta1/byohost_types.go:21`; `controllers/infrastructure/byomachine_controller.go:588` |
| Heartbeat write cadence (agent side) | `agent/reconciler/host_reconciler.go:41,103-109` |
| `AgentConnected` computed from `LastHeartbeatTime` vs. `HeartbeatTimeoutPeriod` (management side) | `controllers/infrastructure/byohost_controller.go:31,94,103-108`; `apis/infrastructure/v1beta1/condition_consts.go:84-96` |
| Agent runs under a process supervisor (systemd, in this repo's reference implementation) with `Restart=always`, a fixed restart delay, and a tight burst limit — no exponential backoff; exact burst/interval values are a deployment tunable, not cited as an architectural constant | current reference implementation's agent unit file (not cited by path — see revision note, this ADR treats the mechanism as general systemd `Restart=`/`StartLimitBurst` behavior, not this repo's specific tuning) |
| Kubelet is its own package, started via a one-shot script the agent runs to completion and exits — never parented or supervised by the agent process | `installer/internal/algo/ubuntu-templates/install.sh.tmpl:50`; `agent/cloudinit/cmd_runner.go:24` |
| Existing opaque-script hand-off pattern this design's *agent-side execution shape* reuses (not its content model) | `agent/reconciler/host_reconciler.go:142-146,181-214` |
| Existing `imgpkg`-pull-then-package-manager-install precedent (used twice already: onboarding and k8s-component install) | `cmd/byohctl/service/agent.go:192-240`; `installer/internal/algo/ubuntu-templates/install.sh.tmpl:9-29` |
| Strict-semver validation precedent this ADR's `TargetVersion` pattern reuses | `hack/version.sh:55` |
| `ByoHost`/`ByoCluster` are namespace-scoped today | `apis/infrastructure/v1beta1/byohost_types.go:117`; `apis/infrastructure/v1beta1/byocluster_types.go:53` |
| Existing docker-backed e2e host harness this plan's tests build on | `test/e2e/docker_helper.go`; `test/e2e/cluster_upgrade_test.go:65-103` |

---

## 8. Open questions

1. **Who produces `PackageURL` for a given `TargetVersion`, and how does it stay in sync with
   published agent builds?** Treated as an opaque input the CR's creator supplies (UI or external
   automation, per this ADR's decision that `TargetVersion` is always explicit — §2.1). In
   practice something — a release process, a UI backed by a build pipeline — needs to produce the
   right URL for a given version and OS/arch cohort. Not decided here.
2. **Package provenance/integrity beyond a plain checksum.** `DesiredAgent.PackageChecksum` catches
   accidental corruption or a wrong URL, not a deliberately malicious artifact — there's no
   signing story. Worth deciding whether this needs to go further, given a compromised agent
   upgrade artifact has a larger blast radius (continuous root execution) than a compromised
   install script (runs once).
3. **RBAC for the new CRD** — which principal may create a `ByoHostAgentUpgrade`. Not
   investigated.
4. **Where should `MaxUnavailable`'s default and `PerHostTimeout`'s default actually land?**
   §2.3/§5.2 specify the mechanism; the numbers need sizing against a real package once one
   exists, informed by real install duration plus the systemd restart-exhaustion window (~20-30s
   per §1.2).
5. **Selector/cohort mismatches aren't caught proactively.** Since cohort-splitting (deb vs. rpm,
   amd64 vs. arm64) is manual (§2.1), nothing stops an operator from pointing a `PackageURL` at
   the wrong cohort — it only surfaces as an install failure per host, not as an upfront
   validation error on the CR. Worth deciding whether the CRD (or an admission webhook) should
   cross-check `Selector`-matched hosts' `HostArchitectureLabel`/`HostOSFamilyLabel` for
   homogeneity before allowing the CR to proceed at all.
