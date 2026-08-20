// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cloudinit

import (
	"context"
	"os"
	"os/exec"
)

//counterfeiter:generate . ICmdRunner
type ICmdRunner interface {
	RunCmd(context.Context, string) error
}

// CmdRunner default implementer of ICmdRunner
// TODO reevaluate empty interface/struct
type CmdRunner struct {
}

// RunCmd executes the command string
func (r CmdRunner) RunCmd(ctx context.Context, cmd string) error {
	command := exec.CommandContext(ctx, "/bin/bash", "-c", cmd) // #nosec G204 -- cmd is admin-authored install/bootstrap content from K8sInstallerConfig/cloud-init, not external/untrusted input
	command.Stderr = os.Stderr
	command.Stdout = os.Stdout
	if err := command.Run(); err != nil {
		return err
	}
	return nil
}

func Pull(ctx context.Context, ref, destDir string) error {
	imgpkgPath, err := exec.LookPath("imgpkg")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, imgpkgPath, "pull", "-i", ref, "-o", destDir) // #nosec G204 -- ref is admin-authored (ByoHostSpec.DesiredAgent.PackageURL), not external/untrusted input
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	return cmd.Run()
}
