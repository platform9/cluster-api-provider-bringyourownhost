// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	// StateFileName is the file in the agent's state directory that records
	// how far an onboarding got. It holds no secrets.
	StateFileName = "state.json"
	// PrivateKeyFileName is the file in the agent's state directory that holds
	// the client private key.
	PrivateKeyFileName = "client.key"

	stateFieldVersion = "version"
	stateFieldPhase   = "phase"

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

// StateFieldError says why one field of the state file is invalid. Callers
// read Field to tell which field it is.
type StateFieldError struct {
	// Field is the JSON name of the invalid field.
	Field string
	// Reason says what is wrong with the value.
	Reason string
}

// Error implements error.
func (e *StateFieldError) Error() string {
	return fmt.Sprintf("field %q: %s", e.Field, e.Reason)
}

// InvalidStateError is returned by loadState when the state file parses but
// has invalid fields. It lists every invalid field, not only the first. Use
// errors.As to reach it, or to reach a single *StateFieldError inside it.
type InvalidStateError struct {
	// Path is the state file that is invalid.
	Path string
	// Problems has one entry for every invalid field.
	Problems []*StateFieldError
}

// Error implements error.
func (e *InvalidStateError) Error() string {
	reasons := make([]string, 0, len(e.Problems))
	for _, problem := range e.Problems {
		reasons = append(reasons, problem.Error())
	}

	return fmt.Sprintf("state file %q is invalid: %s", e.Path, strings.Join(reasons, "; "))
}

// Unwrap returns the field problems, so errors.As finds a *StateFieldError.
func (e *InvalidStateError) Unwrap() []error {
	problems := make([]error, 0, len(e.Problems))
	for _, problem := range e.Problems {
		problems = append(problems, problem)
	}

	return problems
}

// loadState reads the state file in dir. It fails when the file is missing,
// unreadable or not valid JSON. When the file parses but has invalid fields,
// the error is an *InvalidStateError that lists every invalid field.
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

	var problems []*StateFieldError
	if s.Version != stateVersion {
		problems = append(problems, &StateFieldError{Field: stateFieldVersion, Reason: fmt.Sprintf("unknown version %d", s.Version)})
	}
	if s.Phase != PhasePending && s.Phase != PhaseIssued {
		problems = append(problems, &StateFieldError{Field: stateFieldPhase, Reason: fmt.Sprintf("unknown phase %q", s.Phase)})
	}
	if len(problems) > 0 {
		return nil, &InvalidStateError{Path: path, Problems: problems}
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

// matches reports whether the identity in st is for this tenant and host
// name, so a changed --namespace or hostname never reuses an old identity.
func (s *State) matches(tenant, hostName string) error {
	stTenant, stHostName, _, err := infrastructurev1beta1.ParseHostIdentity(s.Identity)
	if err != nil {
		return fmt.Errorf("state has an unusable identity: %w", err)
	}
	if stTenant != tenant || stHostName != hostName {
		return fmt.Errorf("state belongs to tenant %q and host %q, not %q and %q", stTenant, stHostName, tenant, hostName)
	}
	return nil
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
