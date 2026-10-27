package testfixtures_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Check the actual c2j secret contract, including arbitrary map keys and
// workflows nested in arrays, in both source and packaged manifests.
func TestManifestCredentialReferences(t *testing.T) {
	root := filepath.Dir(opRootDir(t))
	tests := []struct {
		op   string
		path []string // "[]" wraps the value in an array.
	}{
		{"jev", []string{"api_key"}},
		{"llm2", []string{"api_keys", "arbitrary_provider"}},
		{"pydantic", []string{"api_keys", "arbitrary_provider"}},
		{"gha", []string{"secrets", "arbitrary_secret"}},
		{"gha-many", []string{"workflows", "[]", "secrets", "arbitrary_secret"}},
		{"aider", []string{"env", "OPENAI_API_KEY"}},
		{"aider", []string{"env", "ANTHROPIC_API_KEY"}},
		{"kimi", []string{"env", "KIMI_MODEL_API_KEY"}},
		{"codex", []string{"api", "api_key"}},
		{"codex", []string{"env", "OPENAI_API_KEY"}},
		{"codex", []string{"env", "CODEX_API_KEY"}},
		{"codex/run_skill", []string{"api", "api_key"}},
		{"codex/run_skill", []string{"env", "OPENAI_API_KEY"}},
		{"codex/run_skill", []string{"env", "CODEX_API_KEY"}},
	}
	for _, tc := range tests {
		for _, format := range []string{"yaml", "json"} {
			t.Run(tc.op+"/"+filepath.Join(tc.path...)+"/"+format, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(root, tc.op, "op."+format))
				require.NoError(t, err)
				var manifest struct {
					InputSchema map[string]any `yaml:"input_schema"`
				}
				require.NoError(t, yaml.Unmarshal(data, &manifest))
				_, err = secrets.ExpandSchema(manifest.InputSchema)
				require.NoError(t, err, "annotations must use supported schema constructs")
				wrap := func(value any) any {
					for i := len(tc.path) - 1; i >= 0; i-- {
						if tc.path[i] == "[]" {
							value = []any{value}
						} else {
							value = map[string]any{tc.path[i]: value}
						}
					}
					return value
				}
				// JSON round-trip matches the durable representation c2j validates.
				encoded, err := json.Marshal(wrap(secrets.Reference{Name: "test_key"}))
				require.NoError(t, err)
				var reference any
				require.NoError(t, json.Unmarshal(encoded, &reference))
				require.NoError(t, secrets.ValidateSchema(manifest.InputSchema, reference, false))
				require.ErrorContains(t, secrets.ValidateSchema(manifest.InputSchema, wrap("test-credential"), false), "requires a reference")
				require.NoError(t, secrets.ValidateSchema(manifest.InputSchema, wrap("test-credential"), true))
				// Public settings and credential variable names remain plain inputs.
				require.NoError(t, secrets.ValidateSchema(manifest.InputSchema, map[string]any{
					"env": map[string]any{"MODEL": "example", "API_BASE": "https://example.test"},
					"api": map[string]any{"api_key_env": "CUSTOM_API_KEY"},
				}, false))
			})
		}
	}
}
