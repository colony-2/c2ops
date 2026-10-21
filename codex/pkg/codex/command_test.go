package codex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexPackagedManifestsNeedNoExternalTools(t *testing.T) {
	for _, name := range []string{"../../op.json", "../../run_skill/op.json"} {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		var manifest struct {
			Dependencies []string `json:"dependencies"`
		}
		require.NoError(t, json.Unmarshal(data, &manifest))
		require.Empty(t, manifest.Dependencies, "packaged ops include the CLI in their Nix closure")
	}
}

func TestCodexCommandUsesExplicitBinaryOrNixPackage(t *testing.T) {
	t.Setenv("C2OPS_CODEX_BIN", "")
	require.Equal(t, []string{"nix", "run", codexPackage, "--", "--version"}, codexCommand("--version"))
	t.Setenv("C2OPS_CODEX_BIN", "/some directory/bin/codex")
	require.Equal(t, []string{"/some directory/bin/codex", "--version"}, codexCommand("--version"))
}

func TestInstructionsOnlyOnInitialCommand(t *testing.T) {
	instructions := "Keep \"quoted\" text, backslashes \\, tabs\tand newlines\nUnicode: λ; control: \v"
	for _, tc := range []struct {
		name, session, instructions string
		want                        bool
	}{
		{"initial", "", instructions, true},
		{"resume ignores changed value", "existing-session", instructions, false},
		{"omitted", "", "", false},
		{"blank", "", " \t\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := buildCommand(Options{Prompt: "user prompt", SessionID: tc.session, Instructions: tc.instructions}, "")
			var found []string
			for i, arg := range args {
				if strings.HasPrefix(arg, "developer_instructions=") {
					require.Greater(t, i, 0)
					require.Equal(t, "-c", args[i-1])
					found = append(found, strings.TrimPrefix(arg, "developer_instructions="))
				}
			}
			if !tc.want {
				require.Empty(t, found)
				return
			}
			require.Len(t, found, 1)
			var decoded string
			require.NoError(t, json.Unmarshal([]byte(found[0]), &decoded))
			require.Equal(t, instructions, decoded)
			require.NotContains(t, found[0], `\v`, "TOML does not recognize Go-only escapes")
			require.Equal(t, "user prompt", args[len(args)-1])
		})
	}
}
