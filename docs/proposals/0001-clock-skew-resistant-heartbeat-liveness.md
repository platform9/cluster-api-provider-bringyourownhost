# ADR 0001: Clock-skew-resistant ByoHost heartbeat liveness

## Status

Accepted — the envtest spike confirmed the `managedFields` behavior this decision depends on. See Spike Results.

Tracks [KAAP-2289](https://platform9.atlassian.net/browse/KAAP-2289).

## Context

The management cluster considers a `ByoHost` connected via `AgentConnected`
([condition_consts.go:84](../../apis/infrastructure/v1beta1/condition_consts.go)),
computed in `ByoHostReconciler.reconcileHeartbeat`
([byohost_controller.go:97](../../controllers/infrastructure/byohost_controller.go)) as:

```go
time.Since(byoHost.Status.LastHeartbeatTime.Time) < r.HeartbeatTimeoutPeriod
```

`LastHeartbeatTime` is written by the host agent using its own clock
(`agent/reconciler/host_reconciler.go:109`), then read back and compared against
`time.Since()`, which uses the management controller's clock. This mixes two
independent clocks in one comparison. The existing code already documents the
assumption this rests on:

> NOTE: LastHeartbeatTime is written by the agent (host clock) and evaluated
> by the management controller (manager clock). Clock skew between the two
> will affect perceived liveness. NTP (or equivalent) synchronization is
> assumed on both the host and management nodes.
> — `agent/reconciler/host_reconciler.go:102-105`

That assumption isn't verified anywhere. When it doesn't hold, the failure is
asymmetric:

- **Host clock behind**: `LastHeartbeatTime` looks stale sooner than it
  should → host is marked `AgentConnected=False` prematurely. Wrong
  diagnosis, but fails safe — a live host just looks falsely offline.
- **Host clock ahead**: `LastHeartbeatTime` is in the future relative to the
  controller. `time.Since()` on a future timestamp is negative, which is
  always `< HeartbeatTimeoutPeriod`. A dead agent's last-written timestamp
  never "goes stale" — the host can stay marked connected indefinitely after
  it has actually died. This fails open, silently.

## Options considered

| # | Option | Why rejected |
|---|---|---|
| 1 | Hard NTP-sync preflight block in `byohctl onboard` | Only guards onboarding-time state; no protection against day-2 drift or NTP stopping later; can't retroactively cover already-onboarded hosts |
| 2 | Same check, warn-only | Same day-2 gap as #1, plus doesn't even block a bad onboarding |
| 3 | Check that an NTP daemon is installed/enabled (not live sync) | "Installed" ≠ "synced"; still onboarding-time only |
| 4 | Live skew measurement against a reference clock at onboarding | Same onboarding-time-only ceiling as 1-3; adds network reachability/timeout/threshold failure modes of its own |
| 5 | Ship `chrony` as a package dependency of the agent deb/rpm | "Installed" ≠ "synced"; only reaches hosts that re-run onboarding, not the existing fleet. Kept as an orthogonal, low-cost hygiene addition, not a fix for this ADR's problem |
| 6 | New `ClockSkewDetected` status condition, agent-reported skew value | Detects/report only, doesn't fix the underlying liveness comparison; a dynamic skew value in the condition message defeats `patch.Helper`'s no-op diffing and risks a write on every reconcile for every host (the exact footgun already flagged at `condition_consts.go:88-93` for `HeartbeatTimeoutReason`) |
| 7 | Do nothing; accept current behavior | Asymmetric failure described above — forward skew fails open indefinitely, which is a real correctness gap, not just noise |
| 8 | New `Status.LastHeartbeatObservedTime` field, stamped by the management controller's own clock when it observes `LastHeartbeatTime` change; liveness compares that instead | Correct, but adds a second time field to `ByoHostStatus` that's written on the same cadence as the existing one |
| 8b | Same idea, tracked in an in-memory map on the reconciler instead of a CR field | No CRD change; at compact encoding (~60-70 bytes/entry) trivially cheap even at 100k+ hosts (~6.5MB). But the map doesn't survive a manager restart or leader-election failover — every host looks "freshly seen" right after, a real (if bounded) regression vs. a persisted signal |
| 9 | Alert on `node_timex_sync_status`/`node_timex_offset_seconds` via Prometheus | This repo has no Prometheus dependency of its own (only the downstream product does); provides no coverage before a host joins a workload cluster; purely observational, doesn't feed back into `ByoHost.Status` or CAPI reconciliation. Recommended as a complementary downstream-product concern, out of scope here |

## Decision

Adopt **8a**: stop comparing the agent's self-reported clock at all for the
liveness decision. Read the *apiserver-authored* timestamp Kubernetes already
attaches to every write — `metadata.managedFields[].time` — for the
managedFields entry corresponding to the agent's field manager's last patch to
`status.lastHeartbeatTime`, and compare that (a management-cluster-clock
timestamp, stamped server-side) against `time.Since()`.

`managedFields` tracking is apiserver-side bookkeeping, GA since Kubernetes
1.22, and applies to every write type (`Update`, JSON merge `Patch`,
strategic merge `Patch`, and SSA `Apply`) — not only to SSA requests. The
agent's writes today go through CAPI's `patch.Helper`
(`sigs.k8s.io/cluster-api v1.4.4`), which issues
`client.Status().Patch(ctx, afterObject, client.MergeFrom(beforeObject))` —
a plain JSON merge patch, not SSA — so this ADR does not assume SSA is in use
anywhere.

`Status.LastHeartbeatTime` is unchanged: the agent keeps writing it every
heartbeat interval as today, host-clock value and all. It now serves purely
as the payload that triggers the apiserver to update `managedFields[].time`,
and remains available for operator-facing display or future skew telemetry.
No new CRD field is introduced.

## Rationale

- **vs. 1-5**: none of the onboarding-time checks protect a host once it's
  running; this fix is continuous and covers the entire host lifecycle.
- **vs. 6**: fixes the actual liveness bug instead of adding a parallel
  detector on top of a still-broken comparison, and avoids designing around
  a write-storm footgun.
- **vs. 7**: closes the forward-skew fail-open gap instead of accepting it.
- **vs. 8**: same correctness guarantee, without adding a second
  time-tracking field to `ByoHostStatus`.
- **vs. 8b**: same zero-new-field property, but fully persisted in etcd via
  the object itself — no state is lost on manager restart or leader
  failover, and no controller memory budget needs to be reasoned about.
- **vs. 9**: this fixes the mechanism itself; a downstream Prometheus alert
  is a reasonable complementary signal for humans, not a substitute for
  correct reconciliation.

## Consequences

**Positive**

- Liveness no longer depends on the host's clock value, in either direction.
- No new CRD field, no new persisted reconciler state, no added apiserver
  call volume — this rides on the write the agent already performs every
  heartbeat interval.
- Protects the entire already-onboarded fleet as soon as the management
  cluster's controller is upgraded; no agent or host-side rollout required.

**Negative / risks**

- Relies on `managedFields`, which is primarily documented as an SSA
  conflict-resolution mechanism rather than a general-purpose application
  API. Confirmed empirically to behave as needed for this repo's specific
  write path against a real apiserver — see Spike Results — but it remains
  less officially "load-bearing" than a field this codebase owns outright.
- For non-Apply requests, the recorded field-manager name comes from the
  writing client's HTTP User-Agent, not an explicit name this code
  currently sets. It must be stable and distinct enough to reliably locate
  the agent's entry among any other writers of the object. Needs
  confirming in production before implementation — see the residual item
  in Spike Results.
- Less discoverable than an explicit status field — nothing in
  `byohost_types.go` hints at this mechanism. Needs a clear comment at the
  point of use in `reconcileHeartbeat`.
- Any code reading `managedFields` for this purpose independently of the
  controller's own watch-triggered `Reconcile` (e.g. a diagnostic script, a
  future test) must account for cache propagation lag if it reads through a
  cached client — see Spike Results for the false-negative this caused
  during testing.

## Spike results

Ran against `envtest`'s real `kube-apiserver` (pinned to Kubernetes v1.25.0
by `scripts/fetch_ext_bins.sh` — well past Server-Side Apply/managedFields
going GA in 1.22, so this is representative apiserver behavior, not a
version-gated fluke). Test: `TestByohostController_HeartbeatManagedFieldsTimeTracksServerWriteTime`
in `controllers/infrastructure/byohost_controller_test.go`.

**First pass produced a false negative.** Reading the object back via
`k8sManager.GetClient()` immediately after each `patch.Helper` write showed
`managedFields[].time` completely unchanged across two writes a second apart
— seemingly proving the mechanism unusable. That client is cache-backed
(informer reads), and an immediate `Get` right after a `Patch` can race the
cache's watch propagation and return a stale snapshot from before the write
landed. Once the test polled with `require.Eventually` instead of trusting a
single read (the same pattern `TestByohostController_UninstallSecretCleanup`
already uses in this file), the result reversed.

**Confirmed, with proper synchronization:**

1. A `patch.Helper` status patch (`client.MergeFrom`, the exact call
   `agent/reconciler/host_reconciler.go` and `agent/registration/host_registrar.go`
   both use) does produce a `managedFields` entry scoped to
   `Subresource: "status"`, with `FieldsV1` correctly narrowed to
   `{"f:status":{".":{},"f:lastHeartbeatTime":{}}}`.
2. That entry's `Time` reflects the apiserver's own clock at write time —
   confirmed by writing a value 24 hours skewed into the field and observing
   `Time` land within seconds of real "now", not anywhere near the skewed
   value.
3. `Time` **does** advance on each subsequent write to the same
   already-owned field path, not only on first ownership — confirmed across
   two writes 1.1+ seconds apart.
4. The recorded manager name for a write is the default derived from the
   writing client's HTTP User-Agent (here, `infrastructure.test`, the
   compiled test binary's default identity) — an explicit override attempted
   via `rest.CopyConfig` + `rest.Config.UserAgent` did not visibly take
   effect within a single test binary hosting two `client.New` instances.
   This is a real open item (see below), not disqualifying: in production
   the agent and the management controller are two separate compiled
   binaries, which get distinct default identities without any explicit
   configuration, unlike two clients constructed side-by-side in one test
   process.

**Why this isn't a concern for the real reconciler.** `main.go` wires
`ByoHostReconciler.Client` to `mgr.GetClient()` — the same kind of
cache-backed client that produced the test's false negative. But the
controller's `Reconcile` calls are triggered by the watch event for the
`ByoHost` object's own change (`ctrl.NewControllerManagedBy(mgr).For(&ByoHost{})`),
so by the time `Reconcile` runs in response to a genuine heartbeat write, the
manager's cache has already ingested that exact write — there's no
independent, out-of-band read to race. The staleness only bit this test's
manual `Get` calls, which don't go through that watch-triggered ordering.

**Residual item before implementation**: confirm what field-manager name the
real agent binary's client presents in production (client-go's default,
derived from the compiled binary's build info via `rest.Config.UserAgent`
when unset — see `agent/main.go:302-309`, which sets no explicit
`UserAgent`), and whether it's stable enough across builds/versions to match
against reliably in `reconcileHeartbeat`, or whether `agent/main.go` should
set an explicit, stable `rest.Config.UserAgent` for this purpose.

## References

- [KAAP-2289](https://platform9.atlassian.net/browse/KAAP-2289) — "Add NTP
  check in byohctl"
- `agent/reconciler/host_reconciler.go:102-109`
- `controllers/infrastructure/byohost_controller.go:97-121`
- `apis/infrastructure/v1beta1/condition_consts.go:84-93`
