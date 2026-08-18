// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"time"

	"github.com/docker/docker/api/types"
)

const (
	// DefaultFileMode the default file mode of files created for tests
	DefaultFileMode fs.FileMode = 0777
)

// Suffixed with the test binary's PID so concurrent Ginkgo nodes (GINKGO_NODES>1)
// don't race on the same debug-script path.
var (
	// ReadByohControllerManagerLogShellFile location of script to read the controller manager log
	ReadByohControllerManagerLogShellFile = fmt.Sprintf("/tmp/read-byoh-controller-manager-log-%d.sh", os.Getpid())
	// ReadAllPodsShellFile location of script to read all pods logs
	ReadAllPodsShellFile = fmt.Sprintf("/tmp/read-all-pods-%d.sh", os.Getpid())
)

// drainTimeout bounds how long stop() waits for the copier goroutine to finish
// after the docker stream is closed, so a wedged stream can't hang a spec.
const drainTimeout = 5 * time.Second

// StreamDockerLog copies the docker stream into outputFile until the stream is closed. Returns a
// func stop, which closes the stream and then waits until the copy has drained and the log file is
// closed. The caller must not close either the stream or the file itself, instead it must invoke
// stop(), optionally as a defer.
func StreamDockerLog(stream types.HijackedResponse, outputFile string) (stop func()) {
	f, err := os.OpenFile(outputFile, os.O_CREATE|os.O_WRONLY, DefaultFileMode) // #nosec G304 -- e2e debug helper; callers always pass a fixed /tmp path owned by the test suite
	if err != nil {
		Showf("OpenFile %s failed, Get err %v", outputFile, err)
		return func() {
			stream.Close()
		}
	}

	done := make(chan struct{})
	go func() {
		// defer calls are LIFO: the log file is closed first, and done is closed after, so
		// stop() only returns once the file is fully written and closed.
		defer close(done)
		defer closeLogFile(f, outputFile)

		// Both results are dropped on purpose: the byte count is uninteresting,
		// and the error is either nil (stream ended on its own) or the
		// closed-connection error that stop() causes by design.
		_, _ = io.Copy(f, stream.Reader)
	}()

	return func() {
		// First close the stream so that it is safe to close the log file.
		stream.Close()
		select {
		// Wait for the copier to drain and close the log file.
		case <-done:
		case <-time.After(drainTimeout):
			Showf("timed out draining docker log into %s", outputFile)
		}
	}
}

// closeLogFile closes f, logging any error against filename
func closeLogFile(f *os.File, filename string) {
	if err := f.Close(); err != nil {
		Showf("error closing file %s: %v", filename, err)
	}
}

// Showf prints formatted string to stdout
func Showf(format string, a ...interface{}) {
	fmt.Printf(format, a...)
	fmt.Printf("\n")
}

// ShowFileContent prints to stdout the content of the given file
func ShowFileContent(fileName string) {
	content, err := os.ReadFile(fileName) // #nosec G304 -- e2e debug helper; callers always pass a fixed /tmp path owned by the test suite
	if err != nil {
		Showf("ioutil.ReadFile %s return failed: Get err %v", fileName, err)
		return
	}

	Showf("######################Start: Content of %s##################", fileName)
	Showf("%s", string(content))
	Showf("######################End: Content of %s##################", fileName)
}

// ExecuteShellScript executes a given shell script file location
func ExecuteShellScript(shellFileName string) {
	// No caller passes a context through this debug-only helper; scope one locally.
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-x", shellFileName) // #nosec G204 -- e2e debug helper; callers always pass a fixed /tmp path owned by the test suite
	output, err := cmd.Output()
	if err != nil {
		Showf("execute %s return failed: Get err %v, output: %s", shellFileName, err, output)
		return
	}
	Showf("#######################Start: execute result of %s##################", shellFileName)
	Showf("%s", string(output))
	Showf("######################End: execute result of %s##################", shellFileName)
}

// WriteShellScript writes shell script contents/commands to the given file location
func WriteShellScript(shellFileName string, shellFileContent []string) {
	f, err := os.OpenFile(shellFileName, os.O_APPEND|os.O_WRONLY|os.O_CREATE, DefaultFileMode) // #nosec G304 -- e2e debug helper; callers always pass a fixed /tmp path owned by the test suite
	if err != nil {
		Showf("Open %s return failed: Get err %v", shellFileName, err)
		return
	}

	defer func() {
		deferredErr := f.Close()
		if deferredErr != nil {
			Showf("Close %s return failed: Get err %v", shellFileName, deferredErr)
		}
	}()

	for _, line := range shellFileContent {
		if _, err = f.WriteString(line); err != nil {
			Showf("Write content %s return failed: Get err %v", line, err)
			return
		}
		if _, err = f.WriteString("\n"); err != nil {
			Showf("Write LF return failed: Get err %v", err)
			return
		}
	}
}

// ShowInfo shows all the pods status, agent logs, and controller manager logs
func ShowInfo(allAgentLogFiles []string) {
	// show swap status
	// showFileContent("/proc/swaps")

	// show the status of  all pods
	shellContent := []string{
		"kubectl get pods --all-namespaces --kubeconfig /tmp/mgmt.conf",
	}
	WriteShellScript(ReadAllPodsShellFile, shellContent)
	ShowFileContent(ReadAllPodsShellFile)
	ExecuteShellScript(ReadAllPodsShellFile)

	// show the agent log
	for _, agentLogFile := range allAgentLogFiles {
		ShowFileContent(agentLogFile)
	}

	// show byoh-controller-manager logs
	shellContent = []string{
		"podNamespace=`kubectl get pods --all-namespaces --kubeconfig /tmp/mgmt.conf | grep byoh-controller-manager | awk '{print $1}'`",
		"podName=`kubectl get pods --all-namespaces --kubeconfig /tmp/mgmt.conf | grep byoh-controller-manager | awk '{print $2}'`",
		"kubectl logs -n ${podNamespace} ${podName} --kubeconfig /tmp/mgmt.conf -c manager",
	}

	WriteShellScript(ReadByohControllerManagerLogShellFile, shellContent)
	ShowFileContent(ReadByohControllerManagerLogShellFile)
	ExecuteShellScript(ReadByohControllerManagerLogShellFile)
}
