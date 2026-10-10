package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuleGateCLIEmitsPolicyFailureWithZeroExit(t *testing.T) {
	cmd := exec.Command("python3", "../scripts/op_test.py", "run", "nix:github:colony-2/c2ops/main#rule_gate")
	cmd.Stdin = stringsReader(`{
		"rules": [
			{
				"id": "validation_passed",
				"type": "assert",
				"value": false,
				"message": "Validation must pass."
			}
		]
	}`)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	var env struct {
		Output struct {
			OK            bool                       `json:"ok"`
			FailedRuleIDs []string                   `json:"failed_rule_ids"`
			Results       map[string]json.RawMessage `json:"results"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.False(t, env.Output.OK)
	require.Equal(t, []string{"validation_passed"}, env.Output.FailedRuleIDs)
	require.Contains(t, env.Output.Results, "validation_passed")
}

func TestRuleGateCLIInvalidInputExitsNonZero(t *testing.T) {
	cmd := exec.Command("python3", "../scripts/op_test.py", "run", "nix:github:colony-2/c2ops/main#rule_gate")
	cmd.Stdin = stringsReader(`{"rules":[]}`)

	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "rules must contain at least one rule")
}

func stringsReader(s string) *strings.Reader {
	return strings.NewReader(s)
}
