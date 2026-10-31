// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: nolintlint,testpackage
package registration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_SaveAndLoad(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "nested", ".byoh")
	want := &State{
		Version:          stateVersion,
		Phase:            PhaseIssued,
		BootstrapTokenID: "abcdef",
		Identity:         "byoh:host:example-worker-1:q4m9z",
		CSRName:          "byoh-csr-example-worker-1-k3v8d",
		CSRUID:           "4b6c",
	}

	err := want.save(dir)
	require.NoError(t, err)

	got, err := loadState(dir)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	info, err := os.Stat(filepath.Join(dir, StateFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// An atomic write renames its temporary file, so only the state file is left.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.Equal(t, []string{StateFileName}, names)
}

func TestState_SaveReplacesExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := &State{Version: stateVersion, Phase: PhasePending, BootstrapTokenID: "abcdef", Identity: "first"}
	second := &State{Version: stateVersion, Phase: PhasePending, BootstrapTokenID: "abcdef", Identity: "second"}

	require.NoError(t, first.save(dir))
	require.NoError(t, second.save(dir))

	got, err := loadState(dir)
	require.NoError(t, err)
	assert.Equal(t, second, got)
}

func TestLoadState_Errors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		contents *string
	}{
		{
			name:     "missing file",
			contents: nil,
		},
		{
			name:     "invalid JSON",
			contents: new(`{"version": 1,`),
		},
		{
			name:     "unknown version",
			contents: new(`{"version": 2, "bootstrapTokenID": "abcdef"}`),
		},
		{
			name:     "no version",
			contents: new(`{"bootstrapTokenID": "abcdef"}`),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tc.contents != nil {
				err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(*tc.contents), 0o600)
				require.NoError(t, err)
			}

			got, err := loadState(dir)
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestLoadState_InvalidFields(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		contents string
		want     *InvalidStateError
		wantErr  bool
	}{
		{
			name:     "wrong version and unknown phase",
			contents: `{"version": 7, "phase": "done"}`,
			want: &InvalidStateError{
				Problems: []*StateFieldError{
					{Field: "version", Reason: `unknown version 7`},
					{Field: "phase", Reason: `unknown phase "done"`},
				},
			},
			wantErr: true,
		},
		{
			name:     "unknown phase only",
			contents: `{"version": 1, "phase": ""}`,
			want: &InvalidStateError{
				Problems: []*StateFieldError{
					{Field: "phase", Reason: `unknown phase ""`},
				},
			},
			wantErr: true,
		},
		{
			name:     "valid fields",
			contents: `{"version": 1, "phase": "pending"}`,
			wantErr:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(tc.contents), 0o600)
			require.NoError(t, err)

			got, err := loadState(dir)

			if !tc.wantErr {
				require.NoError(t, err)
				assert.NotNil(t, got)
				return
			}
			require.Error(t, err)
			assert.Nil(t, got)

			var invalid *InvalidStateError
			require.ErrorAs(t, err, &invalid)
			tc.want.Path = filepath.Join(dir, StateFileName)
			assert.Equal(t, tc.want, invalid)

			var field *StateFieldError
			require.ErrorAs(t, err, &field)
			assert.Equal(t, tc.want.Problems[0], field)
		})
	}

	t.Run("read and parse failures are not invalid state", func(t *testing.T) {
		t.Parallel()

		_, err := loadState(t.TempDir())
		var invalid *InvalidStateError
		assert.False(t, errors.As(err, &invalid))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
}

// TestState_SaveIsAtomicUnderConcurrency runs concurrent writers and readers
// against one state file. A reader must only ever see one complete state that
// a writer saved, never a partial or empty file.
func TestState_SaveIsAtomicUnderConcurrency(t *testing.T) {
	t.Parallel()

	const (
		writers        = 8
		savesPerWriter = 50
		readers        = 4
		// A long identity makes a non-atomic write span several write calls.
		identityPadding = 2000
	)

	dir := t.TempDir()
	newState := func(writer int) *State {
		return &State{
			Version:          stateVersion,
			Phase:            PhasePending,
			BootstrapTokenID: "abcdef",
			Identity:         fmt.Sprintf("writer-%d-%s", writer, strings.Repeat("x", identityPadding)),
		}
	}
	saved := make(map[string]bool, writers)
	for writer := range writers {
		saved[newState(writer).Identity] = true
	}

	// The file exists before any reader starts, so a missing file is a failure.
	err := newState(0).save(dir)
	require.NoError(t, err)

	var writerGroup, readerGroup sync.WaitGroup
	done := make(chan struct{})
	problems := make(chan error, writers*savesPerWriter)

	for writer := range writers {
		writerGroup.Go(func() {
			st := newState(writer)
			for range savesPerWriter {
				if saveErr := st.save(dir); saveErr != nil {
					problems <- saveErr
				}
			}
		})
	}
	for range readers {
		readerGroup.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}

				got, loadErr := loadState(dir)
				switch {
				case loadErr != nil:
					problems <- loadErr
				case !saved[got.Identity]:
					problems <- fmt.Errorf("reader saw a state no writer saved: %q", got.Identity)
				}
			}
		})
	}

	writerGroup.Wait()
	close(done)
	readerGroup.Wait()
	close(problems)

	for problem := range problems {
		assert.NoError(t, problem)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "temporary files are cleaned up")
	assert.Equal(t, StateFileName, entries[0].Name())
}

func TestState_Matches(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		identity string
		wantErr  bool
	}{
		{
			name:     "identity of this tenant and host",
			identity: "byoh:host:" + testTenant + ":" + testHostName + "-abc12",
			wantErr:  false,
		},
		{
			name:     "identity of another tenant",
			identity: "byoh:host:other-tenant:" + testHostName + "-abc12",
			wantErr:  true,
		},
		{
			name:     "identity of another host",
			identity: "byoh:host:" + testTenant + ":other-host-abc12",
			wantErr:  true,
		},
		{
			name:     "unparsable identity",
			identity: "not-an-identity",
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := &State{Identity: tc.identity}

			err := st.matches(testTenant, testHostName)

			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestRemoveOnboardingFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	for _, name := range []string{StateFileName, PrivateKeyFileName, "config"} {
		err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600)
		require.NoError(t, err)
	}

	err := removeOnboardingFiles(dir)
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(dir, StateFileName))
	assert.NoFileExists(t, filepath.Join(dir, PrivateKeyFileName))
	// The final kubeconfig is not part of the onboarding files.
	assert.FileExists(t, config)

	// Files that are already gone are not an error.
	err = removeOnboardingFiles(dir)
	assert.NoError(t, err)
}

func TestLoadState_Phase(t *testing.T) {
	t.Parallel()

	for _, phase := range []Phase{PhasePending, PhaseIssued} {
		t.Run(string(phase)+" is accepted", func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			want := &State{Version: stateVersion, Phase: phase, BootstrapTokenID: "abcdef"}
			require.NoError(t, want.save(dir))

			got, err := loadState(dir)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	testCases := []struct {
		name     string
		contents string
	}{
		{
			name:     "empty phase is rejected",
			contents: `{"version": 1, "phase": "", "bootstrapTokenID": "abcdef"}`,
		},
		{
			name:     "unknown phase is rejected",
			contents: `{"version": 1, "phase": "done", "bootstrapTokenID": "abcdef"}`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(tc.contents), 0o600)
			require.NoError(t, err)

			got, err := loadState(dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown phase")
			assert.Nil(t, got)
		})
	}
}

func TestState_MarkIssued(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	st := &State{
		Version:          stateVersion,
		Phase:            PhasePending,
		BootstrapTokenID: "abcdef",
		Identity:         "byoh:host:example-worker-1:q4m9z",
		CSRName:          "byoh-csr-example-worker-1-k3v8d",
		CSRUID:           "4b6c",
	}

	err := st.markIssued(dir)
	require.NoError(t, err)

	// The CSR the agent created stays named, even though it may be gone from the cluster.
	want := &State{
		Version:          stateVersion,
		Phase:            PhaseIssued,
		BootstrapTokenID: "abcdef",
		Identity:         "byoh:host:example-worker-1:q4m9z",
		CSRName:          "byoh-csr-example-worker-1-k3v8d",
		CSRUID:           "4b6c",
	}
	assert.Equal(t, want, st)
	assert.Equal(t, want, readState(t, dir))
}
