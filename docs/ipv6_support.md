# IPv6 and dual-stack support

BYOH does not have a separate "IPv6 mode" — there's no flag or CRD field to turn on. Addressing is
plain strings throughout (`CONTROL_PLANE_ENDPOINT_IP`, the `ByoCluster` control-plane endpoint, the
agent's own network status), so an IPv6 or dual-stack setup follows the exact same workflow as
[Getting Started](getting_started.md), with an IPv6 literal wherever the docs there say to use an
IP.

This page covers what to expect, what to check before you rely on it, and where to look if
failover doesn't behave the way you expect. It does not introduce a new feature — it documents
behavior that already exists in the current code, verified while auditing IPv6 support.

## What works today

- **Host registration and network status.** The agent enumerates every interface and address on
  the host (`net.Interfaces()`/`iface.Addrs()`), and independently checks for a default route on
  both IPv4 and IPv6. A host with only an IPv6 default route (no IPv4 gateway at all) still
  registers correctly and gets its default interface detected.
- **Control-plane VIP failover.** The kube-vip static pod every BYOH control-plane template ships
  is configured with `vip_arp: "true"`. kube-vip inspects the VIP address itself and automatically
  sends IPv4 gratuitous ARP or IPv6 neighbor advertisements (NDP) accordingly — there is no separate
  flag to set for NDP. This was confirmed by tracing the actual code path kube-vip runs in this
  configuration, and by a live test: bringing up the same kube-vip logic against a real IPv6
  address on an isolated network and confirming (via `tcpdump` and a peer's `ip -6 neigh` table)
  that it sends a real NDP advertisement and that the VIP resolves and responds correctly.

## Setting up an IPv6 or dual-stack control-plane endpoint

Follow [Getting Started](getting_started.md) as normal, with one change: set
`CONTROL_PLANE_ENDPOINT_IP` to an IPv6 literal instead of an IPv4 one.

```shell
CONTROL_PLANE_ENDPOINT_IP=2001:db8::10 clusterctl generate cluster byoh-cluster \
  --infrastructure byoh \
  --kubernetes-version v1.26.6 \
  --control-plane-machine-count 1 \
  --worker-machine-count 1 > cluster.yaml
```

The same constraint that applies to an IPv4 `CONTROL_PLANE_ENDPOINT_IP` applies here: it must be an
address on the same subnet as your control-plane hosts, and not one already in use.

For a multi-node control plane, the hosts also need real IPv6 (or dual-stack) connectivity to each
other on that subnet — same requirement as IPv4, just for the other address family.

## Things to check if the control-plane VIP doesn't fail over

- **The host actually has IPv6 connectivity on the interface kube-vip is using.** kube-vip sends
  its NDP advertisement out the interface named in `vip_interface`
  (the agent's detected default network interface) — confirm the host has a live IPv6 address and
  a working default route on that interface, e.g. `ip -6 addr show <iface>` and
  `ip -6 route show`.
- **NDP is reaching your peers.** Unlike a normal ping response, an *unsolicited* NDP advertisement
  (the kind kube-vip broadcasts on failover) only updates neighbors that already have an entry for
  that address cached — it doesn't necessarily populate a peer's neighbor table from nothing. If a
  fresh client can't reach the VIP right after a failover, that's expected until it actively
  resolves the address (e.g. its own outgoing traffic to the VIP triggers a normal neighbor
  solicitation/advertisement exchange). Check `ip -6 neigh` on a peer after some traffic to the VIP
  has actually flowed, not immediately after failover with no traffic.
- **Firewalls/security groups aren't dropping ICMPv6.** NDP is carried over ICMPv6
  (`Neighbor Solicitation`/`Neighbor Advertisement`); if anything in your network path filters
  ICMPv6, VIP resolution breaks the same way it would break IPv4 ARP if ARP were filtered.

## Known limitations

- **No automated end-to-end test coverage of IPv6/dual-stack clusters in this repo yet.** The
  behavior above is verified at the mechanism level (host network status, kube-vip's ARP/NDP
  dispatch), but there is no CI job that stands up a full IPv6 or dual-stack workload cluster.
  Treat this as "should work, mechanism confirmed correct" rather than "continuously tested."
- **No dual-stack CRD modeling.** `ByoHost`/`ByoMachine`/`ByoCluster` don't track address family as
  a separate field — a cluster is configured with one endpoint address, of whichever family you
  choose. There's no way to expose a control-plane endpoint over both IPv4 and IPv6
  simultaneously through independently-tracked addresses; kubeadm/kubelet own dual-stack cluster
  networking, outside this repo's CRDs. See
  [`docs/proposals/crd-address-family-adr.md`](proposals/crd-address-family-adr.md) for the
  reasoning behind that design choice.
