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

// Exercise omitted API inputs in the documented recipe with c2j's defaults and materializers, then
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
	require.NotContains(t, recipe.Sequence[0].Inputs, "api", "the example must rely on manifest defaults")
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
			explicit, err := rc.ResolveInputs(map[string]any{"api": map[string]any{
				"base_url": `${{ context.config.?codex_api_base_url.orValue(null) }}`,
				"api_key":  `${{ context.secrets.?codex_api_key.orValue(null) }}`,
			}})
			require.NoError(t, err)
			explicitJSON, err := json.Marshal(explicit["api"])
			require.NoError(t, err)
			defaultJSON, err := json.Marshal(input["api"])
			require.NoError(t, err)
			require.JSONEq(t, string(explicitJSON), string(defaultJSON), "defaults must bind the same references as explicit recipe inputs")
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

func TestExternalAPIDefaultOverrides(t *testing.T) {
	t.Setenv("LEGACY_PROXY_KEY", "legacy-key")
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
			for _, tc := range []struct {
				name                      string
				api                       any
				configLookup              bool
				secretNames               []string
				wantURL, wantKey, wantEnv string
			}{
				{name: "null disables all lookups"},
				{name: "empty object uses defaults", api: map[string]any{}, configLookup: true, secretNames: []string{"codex_api_key"}, wantURL: "https://tenant.example/v1", wantKey: "tenant-secret"},
				{name: "explicit URL", api: map[string]any{"base_url": "https://override.example/v1"}, secretNames: []string{"codex_api_key"}, wantURL: "https://override.example/v1", wantKey: "tenant-secret"},
				{name: "explicit secret", api: map[string]any{"api_key": `${{ context.secrets.other_key }}`}, configLookup: true, secretNames: []string{"other_key"}, wantURL: "https://tenant.example/v1", wantKey: "other-secret"},
				{name: "missing explicit secret never uses tenant key", api: map[string]any{"api_key": `${{ context.secrets.?missing_key.orValue(null) }}`}, configLookup: true, secretNames: []string{"missing_key"}},
				{name: "explicit environment overrides default key", api: map[string]any{"api_key_env": "LEGACY_PROXY_KEY"}, configLookup: true, secretNames: []string{"codex_api_key"}, wantURL: "https://tenant.example/v1", wantEnv: "LEGACY_PROXY_KEY"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					submitted := map[string]any{"api": tc.api, "prompt": "test"}
					if strings.Contains(manifest, "run_skill") {
						submitted["skill"] = "test"
					}
					input, err := rc.ResolveInputs(submitted)
					require.NoError(t, err)
					_, err = op.ApplyInvocationDefaults(input)
					require.NoError(t, err)
					configCalls := 0
					configured, _, err := configuration.MaterializeInput(context.Background(), nil, input, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
						configCalls++
						return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{"codex_api_base_url": json.RawMessage(`"https://tenant.example/v1"`)}}, nil
					})
					require.NoError(t, err)
					require.Equal(t, tc.configLookup, configCalls > 0)
					require.NoError(t, op.ValidateInvocationInputs(configured.(map[string]any)))
					refs, err := secrets.InvocationReferences(nil, configured)
					require.NoError(t, err)
					session := secrets.NewSession()
					defer session.Close()
					secretCalls := 0
					require.NoError(t, session.PrepareReferences(context.Background(), refs, func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
						secretCalls++
						require.Equal(t, tc.secretNames, names)
						available := map[string]string{"codex_api_key": "tenant-secret", "other_key": "other-secret"}
						values := map[string]jobdb.SecretValue{}
						for _, name := range names {
							if value, ok := available[name]; ok {
								values[name] = jobdb.SecretValue{Value: value}
							}
						}
						return jobdb.SecretResolveResult{Secrets: values}, nil
					}))
					require.Equal(t, len(tc.secretNames) > 0, secretCalls > 0)
					materialized, err := session.Materialize(configured)
					require.NoError(t, err)
					require.NoError(t, op.ValidateMaterializedInputs(materialized.(map[string]any)))
					payload, err := json.Marshal(materialized)
					require.NoError(t, err)
					var decoded struct {
						API *APIConfig `json:"api"`
					}
					require.NoError(t, json.Unmarshal(payload, &decoded))
					normalized, err := normalizeAPI(decoded.API, nil)
					require.NoError(t, err)
					if tc.wantURL == "" {
						require.Nil(t, normalized)
						return
					}
					require.Equal(t, tc.wantURL, normalized.BaseURL)
					require.Equal(t, tc.wantEnv, normalized.APIKeyEnv)
					if tc.wantKey != "" {
						require.Equal(t, apiTestKey(tc.wantKey), normalized.APIKey)
					} else {
						require.Nil(t, normalized.APIKey)
						require.NotContains(t, buildEnv(Options{API: normalized}), resolvedAPIKeyEnv)
					}
				})
			}
		})
	}
}
