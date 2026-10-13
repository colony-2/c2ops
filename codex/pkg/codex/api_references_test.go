package codex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Exercise the documented recipe with c2j's resolver and materializers, then
// decode precisely the stdin payload that the extension receives.
func TestExternalAPIConfigAndSecretReferences(t *testing.T) {
	recipeBytes, err := os.ReadFile("../../examples/proxy-config.yaml")
	require.NoError(t, err)
	var recipe struct {
		Sequence []struct {
			Inputs map[string]any `yaml:"inputs"`
		} `yaml:"sequence"`
	}
	require.NoError(t, yaml.Unmarshal(recipeBytes, &recipe))
	rc, err := template.NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{})
	require.NoError(t, err)
	for _, manifest := range []string{"../../op.json", "../../run_skill/op.json", "../../op.yaml", "../../run_skill/op.yaml"} {
		t.Run(manifest, func(t *testing.T) {
			raw, err := os.ReadFile(manifest)
			require.NoError(t, err)
			var spec extensions.ExtensionOpSpec
			require.NoError(t, yaml.Unmarshal(raw, &spec))
			op, err := extensions.RestoreResolvedOp(&extensions.ResolvedOp{Spec: spec})
			require.NoError(t, err)
			input, err := rc.ResolveInputs(recipe.Sequence[0].Inputs)
			require.NoError(t, err)
			if strings.Contains(manifest, "run_skill") {
				input["skill"] = "example"
			}
			_, err = op.ApplyInvocationDefaults(input)
			require.NoError(t, err)
			for _, tc := range []struct {
				name              string
				url               json.RawMessage
				key               *string
				external, invalid bool
			}{
				{name: "both present", url: json.RawMessage(`"https://proxy.example/v1"`), key: apiTestKey("private-resolved-key"), external: true},
				{name: "missing config", key: apiTestKey("private-resolved-key")},
				{name: "missing secret", url: json.RawMessage(`"https://proxy.example/v1"`)},
				{name: "both missing"},
				{name: "null config", url: json.RawMessage(`null`), key: apiTestKey("private-resolved-key")},
				{name: "empty config", url: json.RawMessage(`""`), key: apiTestKey("private-resolved-key")},
				{name: "wrong config type", url: json.RawMessage(`false`), key: apiTestKey("private-resolved-key"), invalid: true},
				{name: "empty secret", url: json.RawMessage(`"https://proxy.example/v1"`), key: apiTestKey(""), invalid: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					configured, _, err := configuration.MaterializeInput(context.Background(), nil, input, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
						values := map[string]json.RawMessage{}
						if tc.url != nil {
							values["codex_api_base_url"] = tc.url
						}
						return jobdb.ConfigurationSnapshot{Revision: "test-revision", Values: values}, nil
					})
					require.NoError(t, err)
					if tc.name != "wrong config type" {
						require.NoError(t, op.ValidateInvocationInputs(configured.(map[string]any)))
					}
					refs, err := secrets.InvocationReferences(nil, configured)
					require.NoError(t, err)
					session := secrets.NewSession()
					defer session.Close()
					require.NoError(t, session.PrepareReferences(context.Background(), refs, func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
						require.Equal(t, []string{"codex_api_key"}, names)
						values := map[string]jobdb.SecretValue{}
						if tc.key != nil {
							values["codex_api_key"] = jobdb.SecretValue{Value: *tc.key}
						}
						return jobdb.SecretResolveResult{Secrets: values}, nil
					}))
					materialized, err := session.Materialize(configured)
					require.NoError(t, err)
					err = op.ValidateMaterializedInputs(materialized.(map[string]any))
					if tc.invalid {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					apiInput := materialized.(map[string]any)["api"].(map[string]any)
					require.Contains(t, apiInput, "base_url") // null is retained
					if tc.key == nil {
						require.NotContains(t, apiInput, "api_key")
					}
					payload, err := json.Marshal(materialized)
					require.NoError(t, err)
					var api *APIConfig
					if strings.Contains(manifest, "run_skill") {
						var decoded SkillRunInput
						require.NoError(t, json.Unmarshal(payload, &decoded))
						api = execInputForSkillRun(decoded).API
					} else {
						var decoded ExecOpInput
						require.NoError(t, json.Unmarshal(payload, &decoded))
						api = decoded.API
					}
					normalized, err := normalizeAPI(api, nil)
					require.NoError(t, err)
					opts := testExecuteOptions(t)
					opts.API = normalized
					argv := strings.Join(buildCommand(opts, "/schema.json"), " ")
					childEnv := buildEnv(opts)
					if tc.external {
						require.NotNil(t, normalized)
						require.Equal(t, *tc.key, childEnv[resolvedAPIKeyEnv])
						require.Contains(t, argv, `env_key="`+resolvedAPIKeyEnv+`"`)
					} else {
						require.Nil(t, normalized)
						require.NotContains(t, argv, "c2ops_external")
						require.NotContains(t, childEnv, resolvedAPIKeyEnv)
					}
					require.NotContains(t, argv, "private-resolved-key")
					durable, err := json.Marshal(input)
					require.NoError(t, err)
					require.NotContains(t, string(durable), "private-resolved-key")
				})
			}
			input["api"] = map[string]any{"base_url": "https://proxy.example/v1", "api_key": "literal-key"}
			require.ErrorContains(t, op.ValidateInvocationInputs(input), "requires a reference")
		})
	}
}

func apiTestKey(value string) *string { return &value }
