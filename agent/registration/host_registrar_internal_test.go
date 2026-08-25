// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

func getMockFile(targetOs string) ([]byte, error) {
	out := fmt.Sprintf(`NAME="Ubuntu"
VERSION="20.04.4 LTS (Focal Fossa)"
ID=ubuntu
ID_LIKE=debian
PRETTY_NAME="%s"
VERSION_ID="20.04"
HOME_URL="https://www.ubuntu.com/"
SUPPORT_URL="https://help.ubuntu.com/"
BUG_REPORT_URL="https://bugs.launchpad.net/ubuntu/"
PRIVACY_POLICY_URL="https://www.ubuntu.com/legal/terms-and-policies/privacy-policy"
VERSION_CODENAME=focal
UBUNTU_CODENAME=focal`, targetOs)
	return []byte(out), nil
}

var _ = Describe("Host Registrar Tests", func() {
	Context("When the OS is detected", func() {
		It("Should return the operating system for os following /etc/os-release", func() {
			targetOs := "Ubuntu 20.04.4 LTS"
			detectedOS, err := getOperatingSystem(func(string) ([]byte, error) { return getMockFile(targetOs) })
			Expect(err).ShouldNot(HaveOccurred())
			Expect(detectedOS).To(Equal("Ubuntu 20.04.4 LTS"))
		})

		It("Should return the operating system for os following /usr/lib/os-release", func() {
			targetOs := "Clear Linux Initramfs"
			detectedOS, err := getOperatingSystem(func(releaseFile string) ([]byte, error) {
				if releaseFile == "/etc/os-release" {
					return nil, os.ErrNotExist
				}
				return getMockFile(targetOs)
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(detectedOS).To(Equal("Clear Linux Initramfs"))
		})

		It("Should not error with real hostnamectl", func() {
			_, err := getOperatingSystem(os.ReadFile)
			Expect(err).ShouldNot(HaveOccurred())
		})
	})

	Context("When the os-release file is missing", func() {
		It("Should return error", func() {
			_, err := getOperatingSystem(func(string) ([]byte, error) {
				return nil, os.ErrNotExist
			})
			Expect(err.Error()).To(Equal("error opening file : file does not exist"))
		})
	})

	Context("When the os-release does not contain PRETTY_NAME", func() {
		It("Should return Unknown as operating system", func() {
			detectedOS, err := getOperatingSystem(func(string) ([]byte, error) { return []byte("some_file_without_PRETTY_NAME"), nil })
			Expect(err).ShouldNot(HaveOccurred())
			Expect(detectedOS).To(Equal("Unknown"))
		})
	})

	Context("When the machine ID is read", func() {
		It("Should return the machine ID from /etc/machine-id", func() {
			machineID, err := GetMachineID(func(string) ([]byte, error) { return []byte("deadbeefdeadbeefdeadbeefdeadbeef\n"), nil })
			Expect(err).ShouldNot(HaveOccurred())
			Expect(machineID).To(Equal("deadbeefdeadbeefdeadbeefdeadbeef"))
		})

		It("Should fall back to /var/lib/dbus/machine-id when /etc/machine-id is missing", func() {
			machineID, err := GetMachineID(func(path string) ([]byte, error) {
				if path == "/etc/machine-id" {
					return nil, os.ErrNotExist
				}
				return []byte("cafebabecafebabecafebabecafebabe\n"), nil
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(machineID).To(Equal("cafebabecafebabecafebabecafebabe"))
		})

		It("Should not error with the real machine-id", func() {
			_, err := GetMachineID(os.ReadFile)
			Expect(err).ShouldNot(HaveOccurred())
		})
	})

	Context("When both machine-id locations are missing", func() {
		It("Should return error", func() {
			_, err := GetMachineID(func(string) ([]byte, error) {
				return nil, os.ErrNotExist
			})
			Expect(err.Error()).To(Equal("error opening file : file does not exist"))
		})
	})

	Context("When the OS family is detected", func() {
		It("Should return debian when dpkg is on PATH", func() {
			family := GetOSFamily(func(file string) (string, error) {
				if file == "dpkg" {
					return "/usr/bin/dpkg", nil
				}
				return "", fmt.Errorf("not found")
			})
			Expect(family).To(Equal(infrastructurev1beta1.HostOSFamilyDebian))
		})

		It("Should return rhel when dpkg is absent but rpm is on PATH", func() {
			family := GetOSFamily(func(file string) (string, error) {
				if file == "rpm" {
					return "/usr/bin/rpm", nil
				}
				return "", fmt.Errorf("not found")
			})
			Expect(family).To(Equal(infrastructurev1beta1.HostOSFamilyRHEL))
		})

		It("Should prefer debian when both dpkg and rpm are present", func() {
			family := GetOSFamily(func(string) (string, error) { return "/usr/bin/whatever", nil })
			Expect(family).To(Equal(infrastructurev1beta1.HostOSFamilyDebian))
		})

		It("Should return empty when neither is on PATH", func() {
			family := GetOSFamily(func(string) (string, error) { return "", fmt.Errorf("not found") })
			Expect(family).To(BeEmpty())
		})
	})
})
