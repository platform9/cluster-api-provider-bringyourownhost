// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cloudinit

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

const (
	filePermission = 0644
	dirPermission  = 0744
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . IFileWriter
type IFileWriter interface {
	MkdirIfNotExists(string) error
	WriteToFile(*Files) error
}

// FileWriter default implementation of IFileWriter
type FileWriter struct {
}

// MkdirIfNotExists creates the directory if it does not exist already
func (w FileWriter) MkdirIfNotExists(dirName string) error {
	_, err := os.Stat(dirName)

	if os.IsNotExist(err) {
		return os.MkdirAll(dirName, dirPermission)
	}

	if err != nil {
		return err
	}
	return nil
}

// WriteToFile writes contents to file with appropriate permissions
// as provided in the write_files directive of cloud-config file
func (w FileWriter) WriteToFile(file *Files) error {
	initPermission := fs.FileMode(filePermission)
	if stats, err := os.Stat(file.Path); os.IsExist(err) {
		initPermission = stats.Mode()
	}

	flag := os.O_WRONLY | os.O_CREATE
	if file.Append {
		flag |= os.O_APPEND
	}

	f, err := os.OpenFile(file.Path, flag, initPermission)
	if err != nil {
		return err
	}

	_, err = f.WriteString(file.Content)
	if err != nil {
		return err
	}

	if file.Permissions != "" {
		base := 8
		bitSize := 32
		fileMode, err := strconv.ParseUint(file.Permissions, base, bitSize)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error parse the file permission %s", file.Permissions))
		}

		if err := f.Chmod(fs.FileMode(fileMode)); err != nil {
			return err
		}
	}

	if file.Owner != "" {
		owner := strings.Split(file.Owner, ":")
		base := 10
		bitSize := 32
		ownerFormatLen := 2

		if len(owner) != ownerFormatLen {
			return fmt.Errorf("invalid owner format '%s'", file.Owner)
		}

		userInfo, err := user.Lookup(owner[0])
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error lookup user %s", owner[0]))
		}

		uid, err := strconv.ParseUint(userInfo.Uid, base, bitSize)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error convert uid %s", userInfo.Uid))
		}

		gid, err := strconv.ParseUint(userInfo.Gid, base, bitSize)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error convert gid %s", userInfo.Gid))
		}

		if uid > math.MaxInt64 || gid > math.MaxInt64 {
			return fmt.Errorf("uid or gid value too large for int: uid=%d, gid=%d", uid, gid)
		}

		if err := f.Chown(int(uid), int(gid)); err != nil {
			return err
		}
	}

	return f.Close()
}
