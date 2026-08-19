package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeauthoriseFlags exercises the flag bindings on deauthoriseCmd without
// invoking Run (which os.Exits). We call ParseFlags directly.
func TestDeauthoriseFlags(t *testing.T) {
	origVerbosity := verbosity
	origForce := deauthoriseForce
	t.Cleanup(func() {
		verbosity = origVerbosity
		deauthoriseForce = origForce
	})

	t.Run("registered defaults", func(t *testing.T) {
		// Cobra applies the flag's default at registration time (init()), so
		// asserting the registered DefValue is what documents intent.
		assert.Equal(t, "minimal", deauthoriseCmd.Flags().Lookup("verbosity").DefValue)
		assert.Equal(t, "false", deauthoriseCmd.Flags().Lookup("force").DefValue)
	})

	t.Run("long flags", func(t *testing.T) {
		verbosity = ""
		deauthoriseForce = false
		require.NoError(t, deauthoriseCmd.ParseFlags([]string{"--verbosity", "all", "--force"}))
		assert.Equal(t, "all", verbosity)
		assert.True(t, deauthoriseForce)
	})

	t.Run("short flags", func(t *testing.T) {
		verbosity = ""
		deauthoriseForce = false
		require.NoError(t, deauthoriseCmd.ParseFlags([]string{"-v", "important", "-f"}))
		assert.Equal(t, "important", verbosity)
		assert.True(t, deauthoriseForce)
	})
}
