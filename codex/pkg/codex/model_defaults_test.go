package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestModelEffortDefaults(t *testing.T) {
	rc, err := template.NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{})
	require.NoError(t, err)
	for _, manifest := range []string{"../../op.yaml", "../../op.json", "../../run_skill/op.yaml", "../../run_skill/op.json"} {
		t.Run(manifest, func(t *testing.T) {
			raw, err := os.ReadFile(manifest)
			require.NoError(t, err)
			var spec extensions.ExtensionOpSpec
			require.NoError(t, yaml.Unmarshal(raw, &spec))
			op, err := extensions.RestoreResolvedOp(&extensions.ResolvedOp{Spec: spec})
			require.NoError(t, err)
			for _, field := range []string{"model", "reasoning_effort"} {
				for _, tc := range []struct {
					name     string
					explicit bool
					value    any
					setting  string
					invalid  bool
				}{
					{"default", false, nil, `" configured "`, false},
					{"override", true, " explicit ", `"configured"`, false},
					{"null opt out", true, nil, `"configured"`, false},
					{"empty opt out", true, "", `"configured"`, false},
					{"space opt out", true, " \t ", `"configured"`, false},
					{"missing", false, nil, "", false},
					{"null setting", false, nil, "null", false},
					{"empty setting", false, nil, `""`, false},
					{"space setting", false, nil, `"  "`, false},
					{"bad setting", false, nil, "false", true},
					{"number setting", false, nil, "42", true},
					{"array setting", false, nil, "[]", true},
					{"object setting", false, nil, "{}", true},
					{"number", true, 42, `"configured"`, true},
					{"boolean", true, true, `"configured"`, true},
					{"array", true, []any{}, `"configured"`, true},
					{"object", true, map[string]any{}, `"configured"`, true},
				} {
					t.Run(field+"/"+tc.name, func(t *testing.T) {
						values := map[string]any{"prompt": "test", "skill": "test", "api": nil}
						if !strings.Contains(manifest, "run_skill") {
							delete(values, "skill")
						}
						if tc.explicit {
							values[field] = tc.value
						}
						input, err := rc.ResolveInputs(values)
						require.NoError(t, err)
						_, err = op.ApplyInvocationDefaults(input)
						require.NoError(t, err)
						configured, _, err := configuration.MaterializeInput(context.Background(), nil, input, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
							settings := map[string]json.RawMessage{"codex_model": json.RawMessage(`"other"`), "codex_reasoning_effort": json.RawMessage(`"other"`)}
							delete(settings, "codex_"+field)
							if tc.setting != "" {
								settings["codex_"+field] = json.RawMessage(tc.setting)
							}
							return jobdb.ConfigurationSnapshot{Revision: "test", Values: settings}, nil
						})
						require.NoError(t, err)
						materialized := configured.(map[string]any)
						err = op.ValidateMaterializedInputs(materialized)
						payload, e := json.Marshal(map[string]any{"model": materialized["model"], "reasoning_effort": materialized["reasoning_effort"]})
						require.NoError(t, e)
						var exec ExecOpInput
						var skill SkillRunInput
						e1 := json.Unmarshal(payload, &exec)
						e2 := json.Unmarshal(payload, &skill)
						if tc.invalid {
							require.Error(t, err)
							require.Error(t, e1)
							require.Error(t, e2)
							return
						}
						require.NoError(t, err)
						require.NoError(t, e1)
						require.NoError(t, e2)
						want := ""
						if tc.explicit {
							if v, ok := tc.value.(string); ok {
								want = strings.TrimSpace(v)
							}
						} else if tc.setting != "" && tc.setting != "null" {
							require.NoError(t, json.Unmarshal([]byte(tc.setting), &want))
							want = strings.TrimSpace(want)
						}
						got, other := exec.Model, exec.ReasoningEffort
						if field == "reasoning_effort" {
							got, other = other, got
						}
						require.Equal(t, want, strings.TrimSpace(got))
						require.Equal(t, "other", other)
						require.Equal(t, exec.Model, skill.Model)
						require.Equal(t, exec.ReasoningEffort, skill.ReasoningEffort)
						cmd := buildCommand(Options{Model: exec.Model, ReasoningEffort: exec.ReasoningEffort}, "")
						flag := "--model"
						if field == "reasoning_effort" {
							flag = "model_reasoning_effort="
						}
						require.Equal(t, want != "", strings.Contains(strings.Join(cmd, " "), flag))
					})
				}
			}
			input, err := rc.ResolveInputs(map[string]any{"prompt": "test", "api": nil})
			require.NoError(t, err)
			_, err = op.ApplyInvocationDefaults(input)
			require.NoError(t, err)
			_, _, err = configuration.MaterializeInput(context.Background(), nil, input, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
				return jobdb.ConfigurationSnapshot{}, errors.New("config lookup failed")
			})
			require.ErrorContains(t, err, "config lookup failed")
		})
	}
}

func TestModelEffortCommand(t *testing.T) {
	for _, value := range []string{"", "  "} {
		cmd := strings.Join(buildCommand(Options{Model: value, ReasoningEffort: value}, ""), " ")
		require.NotContains(t, cmd, "--model")
		require.NotContains(t, cmd, "model_reasoning_effort=")
	}
	for _, session := range []string{"", "session-2"} {
		effort := "future\"\\\nvalue"
		cmd := buildCommand(Options{Model: " model with spaces ", ReasoningEffort: " " + effort + " ", SessionID: session, Prompt: "prompt"}, "")
		encoded, _ := json.Marshal(effort)
		require.Contains(t, cmd, "model with spaces")
		require.Contains(t, cmd, "model_reasoning_effort="+string(encoded))
		if session != "" {
			require.Less(t, indexArg(cmd, "model_reasoning_effort="+string(encoded)), indexArg(cmd, "resume"))
		}
	}
}
func indexArg(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}
