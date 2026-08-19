package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecommissionFlags exercises the flag bindings on decommissionCmd without
// invoking Run (which os.Exits). We call ParseFlags directly.
func TestDecommissionFlags(t *testing.T) {
	origVerbosity := verbosity
	origForce := decommissionForce
	t.Cleanup(func() {
		verbosity = origVerbosity
		decommissionForce = origForce
	})

	t.Run("registered defaults", func(t *testing.T) {
		// Cobra applies the flag's default at registration time (init()), so
		// asserting the registered DefValue is what documents intent.
		assert.Equal(t, "minimal", decommissionCmd.Flags().Lookup("verbosity").DefValue)
		assert.Equal(t, "false", decommissionCmd.Flags().Lookup("force").DefValue)
	})

	t.Run("long flags", func(t *testing.T) {
		verbosity = ""
		decommissionForce = false
		require.NoError(t, decommissionCmd.ParseFlags([]string{"--verbosity", "all", "--force"}))
		assert.Equal(t, "all", verbosity)
		assert.True(t, decommissionForce)
	})

	t.Run("short flags", func(t *testing.T) {
		verbosity = ""
		decommissionForce = false
		require.NoError(t, decommissionCmd.ParseFlags([]string{"-v", "important", "-f"}))
		assert.Equal(t, "important", verbosity)
		assert.True(t, decommissionForce)
	})
}
