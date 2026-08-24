# ADR: No address-family-aware fields on ByoHost/ByoMachine/ByoCluster

## Status

Accepted.

## Context

None of this repo's CRDs (`ByoHost`, `ByoMachine`, `ByoCluster`) carry a field that names an
address family (no `IPFamily`/`AddressFamily` enum, no separate IPv4/IPv6 address slots). Every
place addressing information crosses a CRD boundary today uses a plain, family-agnostic string or
string slice:

- `ByoCluster.Spec.ControlPlaneEndpoint` is an `APIEndpoint{Host string, Port int32}`
  ([byocluster_types.go:44-49](../../apis/infrastructure/v1beta1/byocluster_types.go)) — `Host` can
  hold a hostname, an IPv4 literal, or an IPv6 literal interchangeably.
- `ByoHost.EndPointIPAnnotation` ([byohost_types.go:16-17](../../apis/infrastructure/v1beta1/byohost_types.go))
  stores the control-plane endpoint IP as an annotation value (a string), set from
  `Cluster.Spec.ControlPlaneEndpoint.Host` in
  [byomachine_controller.go:608](../../controllers/infrastructure/byomachine_controller.go) and
  consumed by the agent's `deleteEndpointIP`/`deleteIP`
  ([host_reconciler.go:554-561](../../agent/reconciler/host_reconciler.go),
  [host_reconciler_linux.go](../../agent/reconciler/host_reconciler_linux.go)) to clean up a
  leftover VIP.
- `ByoMachine.Status.Network[].IPAddrs` is `[]string`
  ([byomachine_types.go:38-40](../../apis/infrastructure/v1beta1/byomachine_types.go)), mirroring
  the shape of `ByoHost.Status.Network` that the agent populates.

This was checked deliberately, not assumed, while auditing the two related runtime gaps this
address-family work also covers:

- The agent's own network status collection
  (`agent/registration/host_registrar.go`'s `GetNetworkStatus()`) enumerates interfaces via
  `net.Interfaces()`/`iface.Addrs()`, which return both address families without any filtering,
  and (after the accompanying fix to that function) discovers default routes for both IPv4 and
  IPv6 independently. None of this needed a schema change to support both families — it needed a
  bug fix in how failures on one family were handled, not a new field to record which family was
  in play.
- The control-plane VIP failover mechanism (kube-vip, driven by the plain `vip_arp: "true"` flag
  already set in every cluster template) already dispatches internally between ARP and NDP based
  on parsing the address string itself (`vip.IsIPv6()`), not from any family flag this repo passes
  it. `deleteIP`'s use of the same `vip.NewConfig`/`DeleteIP` path inherits that same
  family-agnostic behavior for cleanup, for free.

In both cases, the actual mechanism only ever needed to know the address family long enough to
pick an internal code path (ARP vs. NDP, `/proc/net/route` vs. `/proc/net/ipv6_route`) — and it
determines that itself, by inspecting the address string it was already given, at the point where
the decision is needed. Nothing upstream of that point — reconciler, controller, or CRD — had to
carry the family as a separate, named value for either fix to work.

## Options considered

| # | Option | Why rejected |
|---|---|---|
| 1 | Add an explicit `IPFamily`/`AddressFamily` enum field (e.g. `IPv4`/`IPv6`/`Dual`) to `ByoClusterSpec` and/or `ByoMachineSpec` | No consumer in this codebase branches on address family as a *decision input* — every place that cares (kube-vip, the agent's route discovery) derives it by inspecting the address itself, at the point of use. A CRD field would duplicate information already recoverable from `ControlPlaneEndpoint.Host`/the host's actual interfaces, with no reconciliation logic to keep it correctly in sync against the source of truth |
| 2 | Add typed dual-stack address fields (e.g. separate `HostIPv4`/`HostIPv6` on `APIEndpoint`, mirroring `corev1.Service.Spec.IPFamilies`/`ClusterIPs`) | Solves a problem this repo doesn't have yet: nothing here runs a single cluster reachable over *both* families simultaneously through two independently-tracked addresses. `kubeadm`/`kubelet`, which own the actual cluster networking configuration, already have their own dual-stack modeling outside this repo's CRDs; duplicating a subset of it here would be a second, easily-drifting source of truth for a case with no current caller |
| 3 | Do nothing; leave addressing as plain strings, and write this decision down so it isn't rediscovered as an open question | Matches every finding above: the two real gaps found in this area (default-route discovery silently failing on IPv6-only hosts, and confirming kube-vip's ARP/NDP dispatch) were both runtime bugs/questions, not schema gaps, and both are resolved without touching a CRD |

## Decision

Adopt **3**: no address-family-aware field is added to `ByoHost`, `ByoMachine`, or `ByoCluster`.
Addressing stays exactly as it is today — plain strings and string slices on the CRDs and
annotations, with kubeadm and kubelet (both outside this repo) owning the actual dual-stack
cluster-networking configuration, and kube-vip owning the actual family dispatch for the
control-plane VIP.

## Rationale

- **vs. 1**: a field only earns its place in a CRD if some controller reads it to make a decision.
  No controller or reconciler in this repo does that for address family today — every family
  decision found during this audit is made by inspecting the address value itself, at the single
  point where it matters (kube-vip's `vip.IsIPv6()`, the agent's parallel v4/v6 route-discovery
  calls). Adding the field anyway would mean keeping it correctly derived from
  `ControlPlaneEndpoint.Host` on every relevant reconcile, for a value nothing consumes.
- **vs. 2**: no BYOH cluster today is configured, or has been observed to need to be configured,
  with independently-tracked IPv4 *and* IPv6 endpoints for the same control plane. Modeling that
  now would be speculative dual-stack support for a caller that doesn't exist yet, duplicating
  ground `kubeadm`/`kubelet` already cover.
- **vs. 3 being "no decision" instead of a written one**: the two real gaps this audit found (the
  IPv6-only default-route bug, and the ARP/NDP dispatch question) both looked, from the outside,
  like they might need CRD changes before they were actually traced through the code. Writing this
  down means the next person auditing IPv6 support here doesn't have to re-derive "does this need a
  schema change" from scratch — they can start from "no, and here's why," and re-open this ADR only
  if a real caller for family-aware fields shows up.

## Consequences

**Positive**

- No CRD/CRD-generation churn (`make generate`/`make manifests`) for this decision.
- No new field to keep correctly synchronized against `ControlPlaneEndpoint.Host` or the host's
  actual interfaces on every reconcile.
- Closes an open question a future contributor working on IPv6/dual-stack support would otherwise
  have to re-investigate before making any other change in this area.

**Negative / risks**

- If a real dual-stack use case appears later — a cluster that genuinely needs to expose its
  control-plane endpoint over both families simultaneously, with independent tracking per family —
  this decision will need revisiting. Nothing here forecloses that; it just isn't built preemptively
  for a caller that doesn't exist today.
- Relies on kubeadm/kubelet continuing to own dual-stack cluster networking correctly outside this
  repo; this repo has no test coverage of its own for that boundary, since it doesn't own the
  behavior.

## References

- `apis/infrastructure/v1beta1/byocluster_types.go` — `APIEndpoint`
- `apis/infrastructure/v1beta1/byohost_types.go` — `EndPointIPAnnotation`
- `apis/infrastructure/v1beta1/byomachine_types.go` — `NetworkStatus.IPAddrs`
- `controllers/infrastructure/byomachine_controller.go:608` — annotation write site
- `agent/reconciler/host_reconciler.go:554-561`, `agent/reconciler/host_reconciler_linux.go` —
  `deleteEndpointIP`/`deleteIP`
- `agent/registration/host_registrar.go` — `GetNetworkStatus()`, family-independent default-route
  discovery
