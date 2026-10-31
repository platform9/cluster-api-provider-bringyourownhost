// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// StateFileName is the file in the agent's state directory that records
	// how far an onboarding got. It holds no secrets.
	StateFileName = "state.json"
	// PrivateKeyFileName is the file in the agent's state directory that holds
	// the client private key.
	PrivateKeyFileName = "client.key"

	stateVersion  = 1
	stateFileMode = 0o600
	stateDirMode  = 0o700
)

// Phase says how far an onboarding got.
type Phase string

const (
	// PhasePending means a CSR was created, or is about to be, and the
	// certificate is not yet written to the kubeconfig.
	PhasePending Phase = "pending"
	// PhaseIssued means the certificate is written and the kubeconfig is complete.
	PhaseIssued Phase = "issued"
)

// State is what the agent needs to resume an interrupted onboarding.
type State struct {
	Version int `json:"version"`
	// Phase is how far the onboarding got. It is never empty in a valid state.
	Phase Phase `json:"phase"`
	// BootstrapTokenID is the id half of the bootstrap token. It ties the
	// state to one onboarding. The token secret is never stored.
	BootstrapTokenID string `json:"bootstrapTokenID"`
	// Identity is the certificate common name, fixed for the whole onboarding.
	Identity string `json:"identity"`
	// CSRName is the name of the last CSR the agent created. It changes for
	// every CSR. In the issued phase that CSR may no longer exist in the
	// cluster, because the cluster garbage-collects issued CSRs.
	CSRName string `json:"csrName"`
	// CSRUID is the UID of the CSR named by CSRName. It is empty until that CSR
	// has been created.
	CSRUID string `json:"csrUID"`
}

// loadState reads the state file in dir. It fails when the file is missing,
// unreadable, not valid JSON, of an unknown version or in an unknown phase.
func loadState(dir string) (*State, error) {
	path := filepath.Join(dir, StateFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state file %q: %w", path, err)
	}

	s := &State{}
	if err = json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse state file %q: %w", path, err)
	}
	if s.Version != stateVersion {
		return nil, fmt.Errorf("state file %q has unknown version %d", path, s.Version)
	}
	if s.Phase != PhasePending && s.Phase != PhaseIssued {
		return nil, fmt.Errorf("state file %q has unknown phase %q", path, s.Phase)
	}

	return s, nil
}

// save writes the state file in dir atomically, so a crash leaves either the
// old file or the new one, never a partial file.
func (s *State) save(dir string) (err error) {
	if err = os.MkdirAll(dir, stateDirMode); err != nil {
		return fmt.Errorf("create state directory %q: %w", dir, err)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	tmp, err := os.CreateTemp(dir, StateFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary state file in %q: %w", dir, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if err = tmp.Chmod(stateFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set mode on %q: %w", tmp.Name(), err)
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %q: %w", tmp.Name(), err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync %q: %w", tmp.Name(), err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %q: %w", tmp.Name(), err)
	}

	path := filepath.Join(dir, StateFileName)
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}

	return nil
}

// markIssued records that the certificate was written to the kubeconfig. The
// CSR the agent created stays named in the state.
func (s *State) markIssued(dir string) error {
	s.Phase = PhaseIssued
	return s.save(dir)
}

// removeOnboardingFiles deletes the state file and the private key in dir.
// Files that are already gone are not an error.
func removeOnboardingFiles(dir string) error {
	for _, name := range []string{StateFileName, PrivateKeyFileName} {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q: %w", path, err)
		}
	}

	return nil
}
