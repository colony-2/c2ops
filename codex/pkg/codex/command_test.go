package codex

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexPackageMatchesManifests(t *testing.T) {
	for _, name := range []string{"../../op.json", "../../run_skill/op.json"} {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		var manifest struct {
			Dependencies []string `json:"dependencies"`
		}
		require.NoError(t, json.Unmarshal(data, &manifest))
		require.Contains(t, manifest.Dependencies, "pnpm:"+codexPackage,
			"qualified CLI calls must match the declared package")
	}
}
