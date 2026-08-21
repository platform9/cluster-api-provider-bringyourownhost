// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package reconciler

import "github.com/kube-vip/kube-vip/pkg/vip"

// deleteIP removes the given endpoint IP from iface via kube-vip's ARP
// implementation. Linux-only: kube-vip manages the IP via netlink, which
// has no equivalent outside Linux and is never exercised on a non-Linux
// build in practice, since the agent only ever runs on Linux BYOH hosts.
func deleteIP(ip, iface string) error {
	networks, err := vip.NewConfig(ip, iface, false, "", false, 0, 0, 0, "", "", "", false, nil)
	if err != nil {
		return nil
	}
	if len(networks) == 0 {
		return nil
	}
	_, err = networks[0].DeleteIP()
	return err
}
