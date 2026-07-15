// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/cli/cli/command"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/system"
	"github.com/docker/go-units"
	archive "github.com/moby/go-archive"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega" //nolint: staticcheck
	"github.com/pkg/errors"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/test/framework"
)

const (
	kindImage          = "byoh/node:e2e"
	TempKubeconfigPath = "/tmp/mgmt.conf"
	// ipv4OctetCount is the number of dot-separated octets in an IPv4 subnet (e.g. "10.0.0.0/24").
	ipv4OctetCount = 4
	// kubeconfigFileMode restricts the temp kubeconfig to owner-only access (it contains cluster credentials).
	kubeconfigFileMode os.FileMode = 0600
)

type cpConfig struct {
	followLink bool
	copyUIDGID bool
	sourcePath string
	destPath   string
	container  string
}

// ByoHostRunner runs bring-you-own-host cluster in docker
type ByoHostRunner struct {
	Context                 context.Context
	clusterConName          string
	ByoHostName             string
	PathToHostAgentBinary   string
	Namespace               string
	DockerClient            *client.Client
	NetworkInterface        string
	bootstrapClusterProxy   framework.ClusterProxy
	CommandArgs             map[string]string
	Port                    string
	KubeconfigFile          string
	BootstrapKubeconfigData string
}

// uniqueTempFilePath returns a fresh path from os.CreateTemp without leaving the file open,
// so concurrent Ginkgo nodes staging a kubeconfig don't race on one hardcoded /tmp path.
func uniqueTempFilePath(pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	path := f.Name()
	return path, f.Close()
}

func resolveLocalPath(localPath string) (absPath string, err error) {
	if absPath, err = filepath.Abs(localPath); err != nil {
		return
	}
	return archive.PreserveTrailingDotOrSeparator(absPath, localPath), nil
}

func copyToContainer(ctx context.Context, cli *client.Client, copyConfig cpConfig) (err error) {
	srcPath := copyConfig.sourcePath
	dstPath := copyConfig.destPath

	srcPath, err = resolveLocalPath(srcPath)
	if err != nil {
		return err
	}

	// Prepare destination copy info by stat-ing the container path.
	dstInfo := archive.CopyInfo{Path: dstPath}
	dstStat, err := cli.ContainerStatPath(ctx, copyConfig.container, dstPath)

	// If the destination is a symbolic link, we should evaluate it.
	if err == nil && dstStat.Mode&os.ModeSymlink != 0 {
		linkTarget := dstStat.LinkTarget
		if !system.IsAbs(linkTarget) {
			// Join with the parent directory.
			dstParent, _ := archive.SplitPathDirEntry(dstPath)
			linkTarget = filepath.Join(dstParent, linkTarget)
		}

		dstInfo.Path = linkTarget
		dstStat, _ = cli.ContainerStatPath(ctx, copyConfig.container, linkTarget)
	}

	// Validate the destination path
	if err = command.ValidateOutputPathFileMode(dstStat.Mode); err != nil {
		return errors.Wrapf(err, `destination "%s:%s" must be a directory or a regular file`, copyConfig.container, dstPath)
	}

	// Ignore any error and assume that the parent directory of the destination
	// path exists, in which case the copy may still succeed. If there is any
	// type of conflict (e.g., non-directory overwriting an existing directory
	// or vice versa) the extraction will fail. If the destination simply did
	// not exist, but the parent directory does, the extraction will still
	// succeed.
	if err == nil {
		dstInfo.Exists, dstInfo.IsDir = true, dstStat.Mode.IsDir()
	}

	var (
		content         io.ReadCloser
		resolvedDstPath string
	)

	// Prepare source copy info.
	srcInfo, err := archive.CopyInfoSourcePath(srcPath, copyConfig.followLink)
	if err != nil {
		return err
	}

	srcArchive, err := archive.TarResource(srcInfo)
	if err != nil {
		return err
	}
	defer func() {
		deferredErr := srcArchive.Close()
		if deferredErr != nil {
			Showf("error in closing the src archive %v", deferredErr)
		}
	}()

	// With the stat info about the local source as well as the
	// destination, we have enough information to know whether we need to
	// alter the archive that we upload so that when the server extracts
	// it to the specified directory in the container we get the desired
	// copy behavior.

	// See comments in the implementation of `archive.PrepareArchiveCopy`
	// for exactly what goes into deciding how and whether the source
	// archive needs to be altered for the correct copy behavior when it is
	// extracted. This function also infers from the source and destination
	// info which directory to extract to, which may be the parent of the
	// destination that the user specified.
	dstDir, preparedArchive, err := archive.PrepareArchiveCopy(srcArchive, srcInfo, dstInfo)
	if err != nil {
		return err
	}
	defer func() {
		deferredErr := preparedArchive.Close()
		if deferredErr != nil {
			Showf("error in closing the prepared archive %v", deferredErr)
		}
	}()

	resolvedDstPath = dstDir
	content = preparedArchive

	options := container.CopyToContainerOptions{
		AllowOverwriteDirWithFile: false,
		CopyUIDGID:                copyConfig.copyUIDGID,
	}

	return cli.CopyToContainer(ctx, copyConfig.container, resolvedDstPath, content, options)
}

func (r *ByoHostRunner) createDockerContainer() (container.CreateResponse, error) {
	tmpfs := map[string]string{"/run": "", "/tmp": ""}

	return r.DockerClient.ContainerCreate(r.Context,
		&container.Config{Hostname: r.ByoHostName,
			Image: kindImage,
		},
		&container.HostConfig{Privileged: true,
			SecurityOpt: []string{"seccomp=unconfined"},
			Tmpfs:       tmpfs,
			NetworkMode: container.NetworkMode(r.NetworkInterface),
			Binds:       []string{"/var", "/lib/modules:/lib/modules:ro"},
			// kube-proxy's iptables/netlink usage exhausts Docker's default 1024
			// nofile limit almost immediately; match kind's own node ulimit.
			Resources: container.Resources{
				Ulimits: []*units.Ulimit{
					{
						Name: "nofile",
						Soft: 1048576,
						Hard: 1048576,
					},
				},
			},
		},
		&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{r.NetworkInterface: {}}},
		nil, r.ByoHostName)
}

// raiseInotifyInstanceLimit bumps fs.inotify.max_user_instances inside the container.
//
// Docker's --sysctl rejects fs.inotify.* at container-create time (not on its namespaced-sysctl
// allowlist), so it has to be applied with an exec after the container starts. Running several
// byohost containers alongside the management kind cluster on one Linux VM exhausts the host's
// default of 128 instances, which makes containerd's CRI plugin fail to load ("too many open
// files") and leaves kubeadm join stuck retrying against a dead CRI socket.
func (r *ByoHostRunner) raiseInotifyInstanceLimit(containerID string) error {
	execCommand, err := r.DockerClient.ContainerExecCreate(r.Context, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"sysctl", "-w", "fs.inotify.max_user_instances=8192"},
	})
	if err != nil {
		return errors.Wrapf(err, "create exec for raising inotify instance limit in container %q", containerID)
	}
	return errors.Wrapf(r.DockerClient.ContainerExecStart(r.Context, execCommand.ID, container.ExecStartOptions{}),
		"raise inotify instance limit in container %q", containerID)
}

func (r *ByoHostRunner) copyKubeconfig(config cpConfig, listopt container.ListOptions) error {
	var kubeconfig []byte
	if r.NetworkInterface == "host" {
		listopt.Filters.Add("name", r.ByoHostName)
		containers, err := r.DockerClient.ContainerList(r.Context, listopt)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(containers)).To(Equal(1))

		kubeconfig, err = os.ReadFile(r.KubeconfigFile)
		Expect(err).NotTo(HaveOccurred())

		re := regexp.MustCompile("server:.*")
		kubeconfig = re.ReplaceAll(kubeconfig, []byte("server: https://127.0.0.1:"+r.Port))
		Expect(os.WriteFile(TempKubeconfigPath, kubeconfig, kubeconfigFileMode)).NotTo(HaveOccurred()) // #nosec G703 -- TempKubeconfigPath is a fixed local const, not user input

		// If the --bootstrap-kubeconfig is not provided, the tests will use
		// kubeconfig placed in ~/.byoh/config
		if r.CommandArgs["--bootstrap-kubeconfig"] == "" {
			// get the $HOME env variable to set the destination path for kubeconfig
			execCommand, err := r.DockerClient.ContainerExecCreate(r.Context, containers[0].ID, container.ExecOptions{
				AttachStdin:  false,
				AttachStdout: true,
				AttachStderr: true,
				Cmd:          []string{"sh", "-c", "echo ${HOME}"},
			})
			Expect(err).ShouldNot(HaveOccurred())
			resp, err := r.DockerClient.ContainerExecAttach(r.Context, execCommand.ID, container.ExecAttachOptions{})
			Expect(err).ShouldNot(HaveOccurred())
			defer resp.Close()
			homeDir, err := resp.Reader.ReadString('\n')
			Expect(err).ShouldNot(HaveOccurred())
			homeDir = strings.TrimSuffix(homeDir, "\n")
			// create the directory to place the kubeconfig
			execCommand, err = r.DockerClient.ContainerExecCreate(r.Context, containers[0].ID, container.ExecOptions{
				AttachStdin:  false,
				AttachStdout: true,
				AttachStderr: true,
				Cmd:          []string{"sh", "-c", "mkdir ${HOME}/.byoh"},
			})
			Expect(err).ShouldNot(HaveOccurred())
			err = r.DockerClient.ContainerExecStart(r.Context, execCommand.ID, container.ExecStartOptions{})
			Expect(err).ShouldNot(HaveOccurred())

			config.sourcePath = TempKubeconfigPath
			// SplitAfterN used to remove the unwanted special characters in the homeDir
			config.destPath = strings.SplitAfterN(strings.TrimSpace(homeDir)+"/.byoh/config", "/", 2)[1] //nolint: mnd
		} else {
			config.sourcePath = TempKubeconfigPath
			config.destPath = r.CommandArgs["--bootstrap-kubeconfig"]
		}
	} else {
		listopt.Filters.Add("name", r.clusterConName+"-control-plane")
		containers, err := r.DockerClient.ContainerList(r.Context, listopt)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(containers)).To(Equal(1))
		profile, err := r.DockerClient.ContainerInspect(r.Context, containers[0].ID)
		Expect(err).NotTo(HaveOccurred())

		kubeconfig := []byte(r.BootstrapKubeconfigData)

		re := regexp.MustCompile("server:.*")
		kubeconfig = re.ReplaceAll(kubeconfig, []byte("server: https://"+profile.NetworkSettings.Networks[r.NetworkInterface].IPAddress+":6443"))
		config.destPath = r.CommandArgs["--bootstrap-kubeconfig"]

		bootstrapKubeconfigFileData, err := clientcmd.Load(kubeconfig)
		Expect(err).NotTo(HaveOccurred())

		bootstrapKubeconfigPath, err := uniqueTempFilePath("bootstrap-kubeconfig-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			if removeErr := os.Remove(bootstrapKubeconfigPath); removeErr != nil {
				Showf("error removing temp kubeconfig file %s: %v", bootstrapKubeconfigPath, removeErr)
			}
		}()

		err = clientcmd.WriteToFile(*bootstrapKubeconfigFileData, bootstrapKubeconfigPath)
		Expect(err).ShouldNot(HaveOccurred())

		config.sourcePath = bootstrapKubeconfigPath
	}
	err := copyToContainer(r.Context, r.DockerClient, config)
	return err
}

// SetupByoDockerHost sets up the byohost docker container
func (r *ByoHostRunner) SetupByoDockerHost() (*container.CreateResponse, error) {
	var byohost container.CreateResponse
	var err error

	byohost, err = r.createDockerContainer()

	Expect(err).NotTo(HaveOccurred())
	Expect(r.DockerClient.ContainerStart(r.Context, byohost.ID, container.StartOptions{})).NotTo(HaveOccurred())
	Expect(r.raiseInotifyInstanceLimit(byohost.ID)).To(Succeed())

	config := cpConfig{
		sourcePath: r.PathToHostAgentBinary,
		destPath:   "/agent",
		container:  byohost.ID,
	}
	Expect(copyToContainer(r.Context, r.DockerClient, config)).NotTo(HaveOccurred())

	listopt := container.ListOptions{}
	listopt.Filters = filters.NewArgs()

	err = r.copyKubeconfig(config, listopt)
	return &byohost, err
}

// ExecByoDockerHost runs the exec command in the byohost docker container
func (r *ByoHostRunner) ExecByoDockerHost(byohost *container.CreateResponse) (types.HijackedResponse, string, error) {
	var cmdArgs []string
	cmdArgs = append(cmdArgs, "./agent")
	for flag, arg := range r.CommandArgs {
		cmdArgs = append(cmdArgs, flag, arg)
	}
	rconfig := container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmdArgs,
	}

	resp, err := r.DockerClient.ContainerExecCreate(r.Context, byohost.ID, rconfig)
	Expect(err).NotTo(HaveOccurred())

	output, err := r.DockerClient.ContainerExecAttach(r.Context, resp.ID, container.ExecAttachOptions{})
	return output, byohost.ID, err
}

// controlPlaneEndpointIPBaseOctet is the lowest last-octet value handed out for a control-plane
// endpoint IP (process 1 gets this value, process 2 gets this+1, and so on). It's a static IP in
// the kind network's subnet but outside its DHCP range.
const controlPlaneEndpointIPBaseOctet = 151

// controlPlaneEndpointIP derives a control-plane endpoint IP from the kind network's subnet,
// offset by processIndex so that concurrently running Ginkgo processes (GINKGO_NODES>1) each get
// their own unique endpoint IP instead of colliding on the same static address.
func controlPlaneEndpointIP(subnet string, processIndex int) (string, error) {
	ipOctets := strings.Split(subnet, ".")
	if len(ipOctets) < ipv4OctetCount {
		return "", fmt.Errorf("unexpected subnet format: %s", subnet)
	}

	ipOctets[3] = strconv.Itoa(controlPlaneEndpointIPBaseOctet + processIndex - 1)
	return strings.Join(ipOctets[:ipv4OctetCount], "."), nil
}

func setControlPlaneIP(ctx context.Context, dockerClient *client.Client) {
	_, ok := os.LookupEnv("CONTROL_PLANE_ENDPOINT_IP")
	if ok {
		return
	}
	inspect, _ := dockerClient.NetworkInspect(ctx, "kind", network.InspectOptions{})

	// kind networks may include IPv6 IPAM configs; find the first IPv4 subnet.
	var ipv4Subnet string
	for _, cfg := range inspect.IPAM.Config {
		if strings.Contains(cfg.Subnet, ".") {
			ipv4Subnet = cfg.Subnet
			break
		}
	}
	Expect(ipv4Subnet).NotTo(BeEmpty(), "no IPv4 subnet found in kind network IPAM config")

	ip, err := controlPlaneEndpointIP(ipv4Subnet, ginkgo.GinkgoParallelProcess())
	Expect(err).NotTo(HaveOccurred())

	err = os.Setenv("CONTROL_PLANE_ENDPOINT_IP", ip)
	if err != nil {
		Expect(err).NotTo(HaveOccurred())
	}
}
