# Agent Self-Upgrade

This document describes how to upgrade the BYOH host agent binary on already-registered hosts,
staged across a fleet from the management cluster. It hasn't yet been exercised against a real
multi-host cluster end to end — validate it in a non-production environment before relying on it
for a production fleet. For the design rationale, alternatives considered, and open questions, see
[docs/proposals/agent-self-upgrade-adr.md](proposals/agent-self-upgrade-adr.md) — this doc is the
practical "how it works and how to use it" reference; the ADR is the "why."

## What it does

An operator (or automation) declares a target agent version and a package to install for a subset
of `ByoHost`s. Each selected host pulls that package, installs it with its own package manager,
and restarts into the new binary. A rollout controller stages this across the selected hosts with
a configurable concurrency limit, and halts on the first explicit failure rather than continuing
past a bad rollout.

There is no in-agent self-download/self-swap logic and no arbitrary script execution: the agent's
job is limited to pulling one OCI-packaged artifact and handing it to `dpkg`/`rpm`.

## How a single host upgrades

A single optional `desiredAgent` field on `ByoHostSpec` drives a host's own upgrade
(`apis/infrastructure/v1beta1/byohost_types.go`) — nil means no managed upgrade is in progress:

| Field | Meaning |
|---|---|
| `desiredAgent.version` | The agent version this host should be running. Compared against the agent's own build version every reconcile tick. |
| `desiredAgent.packageURL` | An OCI image reference — pulled via `imgpkg pull`, the same mechanism already used for k8s-component bundles — for a bundle containing the `.deb`/`.rpm` that installs `desiredAgent.version`. Pin this by digest (`@sha256:...`) for integrity; it's not templated and doesn't point at a Secret. |
| `desiredAgent.packageChecksum` | Optional `sha256:<hex>` checksum of the specific `.deb`/`.rpm` extracted from the bundle (not the OCI image itself — digest-pinning the reference already covers that). |
| `desiredAgent.assignedAt` | When the rollout controller assigned this upgrade. Only the controller reads this (to enforce `perHostTimeout`, below); the agent ignores it. |

All four are always set together, as one object, by the `ByoHostAgentUpgrade` rollout controller
(below) — not by hand.

On a mismatch between `desiredAgent.version` and its own version, the agent:

1. Waits if `desiredAgent.packageURL` isn't set yet.
2. Runs `imgpkg pull -i <desiredAgent.packageURL> -o <tempdir>`, then finds the single `*.deb` or
   `*.rpm` in the pulled bundle matching its own already-detected package manager. Zero or more
   than one match is an error — the agent doesn't guess.
3. Verifies `desiredAgent.packageChecksum` against that file, if set.
4. Runs `dpkg -i <path>` or `rpm -Uvh <path>` — a fixed, two-way branch based on the host's own
   package manager, never an operator-supplied command. **No `--oldpackage`/force-downgrade flag
   is ever passed.** A host pointed at an older version than it's currently running will fail this
   step by design — downgrading a host that runs continuously as root is treated as something that
   always needs a human, never automated. See "Rollback" below.
5. On success, exits (`os.Exit(0)`); the process supervisor (`Restart=always` in
   `service/pf9-byohostagent.service`) relaunches it, which by then is running the new binary. See
   the ADR §1.2 for the systemd `RestartSec`/`StartLimitBurst`/`StartLimitIntervalSec` tuning this
   relies on — don't shrink `StartLimitIntervalSec` back toward `RestartSec` if you ever touch that
   unit file, it silently disables the crash-loop protection.
6. On any failure, the host's `AgentUpgradeSucceeded` condition is set `False` with one of these
   reasons, and nothing further is attempted automatically:

   | Reason | Meaning |
   |---|---|
   | `AgentPackagePullFailed` | `imgpkg pull` failed (bad reference, registry unreachable, `imgpkg` missing on the host). |
   | `PackageBundleInvalid` | The pulled bundle didn't contain exactly one artifact matching this host's package family. |
   | `PackageChecksumMismatch` | The extracted artifact didn't match `desiredAgent.packageChecksum`. |
   | `PackageInstallFailed` | `dpkg`/`rpm` exited non-zero. |

   `desiredAgent` is left untouched on failure — retrying is the rollout controller's job, not the
   agent's.

Two labels are set automatically on every `ByoHost` at registration
(`agent/registration/host_registrar.go`), used for targeting a `ByoHostAgentUpgrade` at a specific
cohort in a mixed fleet:

| Label | Values | Set from |
|---|---|---|
| `byoh.infrastructure.cluster.x-k8s.io/architecture` | e.g. `amd64`, `arm64` | `runtime.GOARCH` |
| `byoh.infrastructure.cluster.x-k8s.io/os-family` | `debian` or `rhel` | probing for `dpkg`/`rpm` on `PATH` |

## Staging an upgrade across a fleet: `ByoHostAgentUpgrade`

`ByoHostAgentUpgrade` is a namespaced CRD. Its namespace **is** the scope of the rollout — there's
no separate tenant/cohort concept. Use `spec.selector` to narrow further within that namespace
(e.g. by the architecture/OS-family labels above, for a mixed fleet, or by
`cluster.x-k8s.io/cluster-name` for one `ByoCluster`'s hosts).

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoHostAgentUpgrade
metadata:
  name: agent-upgrade-v1-2-0
  namespace: default
spec:
  selector:
    matchLabels:
      byoh.infrastructure.cluster.x-k8s.io/os-family: debian
      byoh.infrastructure.cluster.x-k8s.io/architecture: amd64
  targetVersion: v1.2.0
  packageURL: registry.example.com/byoh-agent-bundle@sha256:...
  packageChecksum: "sha256:..."   # optional
  maxUnavailable: 1                # optional, defaults to 1
  perHostTimeout: 10m              # optional, defaults to 10m
```

`targetVersion` must be an explicit, fully-resolved version — the API rejects `latest` or any
other non-semver value at admission (`+kubebuilder:validation:Pattern` on the field), so a typo or
a floating tag fails fast rather than silently doing nothing.

If your fleet mixes package formats or architectures, create one `ByoHostAgentUpgrade` per cohort,
each with its own `selector` and `packageURL` — there is no automatic per-host resolution of the
right artifact; that split is manual and deliberate (see the ADR for why).

### Status

```yaml
status:
  phase: Progressing        # Pending | Progressing | Completed | Failed
  total: 4                  # hosts currently matching selector
  upgraded: 2                # hosts converged on targetVersion
  unavailableCount: 1        # see "What counts as unavailable" below
  failedHosts: []            # non-empty only once phase is Failed
  conditions:
    - type: RolloutAvailable
      status: "True"
```

Per-host timing (when a host was assigned this rollout's `targetVersion`, which `perHostTimeout`
measures against) lives on that host's own `ByoHost.spec.desiredAgent.assignedAt`, not here — the
rollout controller stamps it in the same write as `version`/`packageURL`/`packageChecksum`.

### What counts as "unavailable," and why it matters

The rollout never has more than `maxUnavailable` hosts simultaneously **unavailable**, where
unavailable is the union of two sets, not their sum — a host in both only counts once:

- **In flight**: assigned this rollout's `targetVersion` but not yet converged, and not yet marked
  failed.
- **Disconnected**: `AgentConnected` is not `True` — computed across *every* selector-matched host,
  whether this rollout has touched it yet or not. This is what lets the rollout notice a host that
  already converged and later crashed, and pause before starting the next batch, without needing a
  bake/soak timer.

Two distinct "can't proceed right now" states exist, and they behave differently:

- **Blocked (soft, self-healing)**: unavailable count has hit the limit, but nothing has explicitly
  failed. `RolloutAvailable` goes `False` with reason `InsufficientAvailabilityBudget`. No hosts
  are picked, but nothing is marked failed either — if a disconnected host recovers (even one
  entirely unrelated to this rollout — down for its own hardware reasons before the CR was even
  created), the budget frees up and picking resumes automatically on the next reconcile. This can
  make a rollout appear stuck at `Pending` indefinitely if a selector-matched host is unhealthy for
  an unrelated reason — check `RolloutAvailable`'s message before assuming a bug.
- **Failed (hard, terminal)**: a host's `AgentUpgradeSucceeded` condition goes `False`, or it stays
  in-flight past `perHostTimeout`. `phase` becomes `Failed` permanently — **no further hosts are
  ever selected by this object again**, and already-upgraded hosts are left as-is. There is no
  automatic retry and no automatic rollback of any kind. To retry, fix the underlying problem
  (e.g. a corrected `packageURL`) and create a **new** `ByoHostAgentUpgrade`.

## Rollback

Always manual, never automated — not via this CRD, not per-host, not even as a "fix and resume"
button on the failed object. There are two reasons, both deliberate:

1. The agent's install step never passes `--oldpackage`, so a `ByoHostAgentUpgrade` whose
   `targetVersion` is older than a host's current version will simply fail that host's install
   every time.
2. Even for a host stuck after a bad upgrade, recovery is the same as any other broken-agent
   scenario: SSH/Ansible. Nothing in this system creates a rollback CR on its own initiative.

If you need to move a fleet backward, that's an out-of-band operator action, not something to
express as a `ByoHostAgentUpgrade` with a lower `targetVersion`.

## Troubleshooting

- **Rollout stuck at `Pending`, `RolloutAvailable=False`**: some selector-matched host is
  unavailable — check its `AgentConnected` condition. This is usually unrelated to the upgrade
  itself (e.g. a host that was already down); fix that host and the rollout resumes on its own.
- **`Phase: Failed`**: check `status.failedHosts` and each host's `AgentUpgradeSucceeded` condition
  reason (table above) for why. Fix the root cause, then create a new `ByoHostAgentUpgrade` — this
  one will never pick up new hosts again.
- **A host's agent doesn't come back after an upgrade**: it may be crash-looping locally. Check
  `systemctl status pf9-byohost-agent` and `journalctl -u pf9-byohost-agent` on the host — the
  systemd unit gives it a bounded number of automatic restarts (see
  `service/pf9-byohostagent.service` and the ADR §1.2) before it needs `systemctl reset-failed` and
  manual intervention.
