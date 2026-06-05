package rulegate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateReturnsResultsMapForFailedRulesOnly(t *testing.T) {
	root := testRoots(t)
	writeFile(t, filepath.Join(root.inbox, "reviews", "review-pack.json"), `{not json`)

	output, err := Evaluate(context.Background(), Input{
		ArtifactInboxPath: root.inbox,
		WorktreePath:      root.worktree,
		Rules: []Rule{
			{
				ID:       "review_pack_artifact_exists",
				Type:     RuleArtifactExists,
				Message:  "Review pack artifact is required.",
				Artifact: rawValue(t, `{"kind":"artifact","name":"reviews/review-pack.json"}`),
			},
			{
				ID:        "review_pack_json",
				Type:      RuleJSONParse,
				Message:   "Review pack must be valid JSON.",
				InboxPath: "reviews/review-pack.json",
			},
		},
	})
	require.NoError(t, err)

	require.False(t, output.OK)
	require.Equal(t, []string{"review_pack_json"}, output.FailedRuleIDs)
	require.Equal(t, Summary{Total: 2, Passed: 1, Failed: 1}, output.Summary)
	require.Contains(t, output.Results, "review_pack_json")
	require.NotContains(t, output.Results, "review_pack_artifact_exists")
	require.Equal(t, RuleJSONParse, output.Results["review_pack_json"].Type)
	require.Equal(t, "failed", output.Results["review_pack_json"].Status)
	require.Equal(t, "reviews/review-pack.json", output.Results["review_pack_json"].Details["inbox_path"])
}

func TestAssertRule(t *testing.T) {
	value := false
	output, err := Evaluate(context.Background(), Input{
		Rules: []Rule{{
			ID:      "validation_passed",
			Type:    RuleAssert,
			Message: "Validation must pass.",
			Value:   &value,
		}},
	})
	require.NoError(t, err)
	require.False(t, output.OK)
	require.Equal(t, []string{"validation_passed"}, output.FailedRuleIDs)
	require.Equal(t, false, output.Results["validation_passed"].Details["value"])
}

func TestValidateDuplicateRuleIDs(t *testing.T) {
	value := true
	_, err := Evaluate(context.Background(), Input{
		Rules: []Rule{
			{ID: "same", Type: RuleAssert, Message: "one", Value: &value},
			{ID: "same", Type: RuleAssert, Message: "two", Value: &value},
		},
	})
	require.ErrorContains(t, err, `duplicate rule id "same"`)
}

func TestArtifactExistsRule(t *testing.T) {
	tests := []struct {
		name     string
		artifact JSONValue
		wantOK   bool
	}{
		{name: "object", artifact: rawValue(t, `{"kind":"artifact"}`), wantOK: true},
		{name: "string", artifact: rawValue(t, `"artifact-ref"`), wantOK: true},
		{name: "null", artifact: rawValue(t, `null`), wantOK: false},
		{name: "empty string", artifact: rawValue(t, `""`), wantOK: false},
		{name: "empty object", artifact: rawValue(t, `{}`), wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, err := Evaluate(context.Background(), Input{
				Rules: []Rule{{
					ID:       "artifact_present",
					Type:     RuleArtifactExists,
					Message:  "Artifact is required.",
					Artifact: tc.artifact,
				}},
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantOK, output.OK)
		})
	}
}

func TestArtifactExistsRejectsOmittedArtifact(t *testing.T) {
	_, err := Evaluate(context.Background(), Input{
		Rules: []Rule{{
			ID:      "artifact_present",
			Type:    RuleArtifactExists,
			Message: "Artifact is required.",
		}},
	})
	require.ErrorContains(t, err, "artifact is required")
}

func TestFileExistsUsesWorktreePath(t *testing.T) {
	root := testRoots(t)
	writeFile(t, filepath.Join(root.worktree, "go.mod"), "module example\n")
	writeFile(t, filepath.Join(root.inbox, "go.mod"), "module wrong\n")

	output, err := Evaluate(context.Background(), Input{
		WorktreePath: root.worktree,
		Rules: []Rule{{
			ID:      "go_mod_exists",
			Type:    RuleFileExists,
			Message: "go.mod is required.",
			Path:    "go.mod",
		}},
	})
	require.NoError(t, err)
	require.True(t, output.OK)
	require.Empty(t, output.Results)
}

func TestPathValidationRejectsTraversal(t *testing.T) {
	root := testRoots(t)
	_, err := Evaluate(context.Background(), Input{
		WorktreePath: root.worktree,
		Rules: []Rule{{
			ID:      "bad_path",
			Type:    RuleFileExists,
			Message: "Bad path.",
			Path:    "../go.mod",
		}},
	})
	require.ErrorContains(t, err, "must not contain ..")
}

func TestJSONParseMissingAndMalformedArePolicyFailures(t *testing.T) {
	root := testRoots(t)
	writeFile(t, filepath.Join(root.inbox, "bad.json"), `{bad`)

	output, err := Evaluate(context.Background(), Input{
		ArtifactInboxPath: root.inbox,
		Rules: []Rule{
			{ID: "missing_json", Type: RuleJSONParse, Message: "Missing JSON.", InboxPath: "missing.json"},
			{ID: "bad_json", Type: RuleJSONParse, Message: "Bad JSON.", InboxPath: "bad.json"},
		},
	})
	require.NoError(t, err)
	require.False(t, output.OK)
	require.Equal(t, []string{"missing_json", "bad_json"}, output.FailedRuleIDs)
	require.Contains(t, output.Results["missing_json"].Details["error"], "does not exist")
	require.Contains(t, output.Results["bad_json"].Details["error"], "invalid character")
}

func TestJSONSchemaInlineSchema(t *testing.T) {
	root := testRoots(t)
	writeFile(t, filepath.Join(root.inbox, "person.json"), `{"name":123}`)

	output, err := Evaluate(context.Background(), Input{
		ArtifactInboxPath: root.inbox,
		Rules: []Rule{{
			ID:        "person_schema",
			Type:      RuleJSONSchema,
			Message:   "Person must match schema.",
			InboxPath: "person.json",
			Schema: rawValue(t, `{
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"required":["name"]
			}`),
		}},
	})
	require.NoError(t, err)
	require.False(t, output.OK)
	require.Contains(t, output.Results["person_schema"].Details["error"], "name")
}

func TestJSONSchemaSchemaSourceValidation(t *testing.T) {
	root := testRoots(t)
	_, err := Evaluate(context.Background(), Input{
		ArtifactInboxPath: root.inbox,
		WorktreePath:      root.worktree,
		Rules: []Rule{{
			ID:                 "schema_sources",
			Type:               RuleJSONSchema,
			Message:            "Schema source.",
			InboxPath:          "input.json",
			Schema:             rawValue(t, `{"type":"object"}`),
			SchemaWorktreePath: "schema.json",
		}},
	})
	require.ErrorContains(t, err, "exactly one")
}

func TestJSONSchemaRejectsExternalRefs(t *testing.T) {
	root := testRoots(t)
	writeFile(t, filepath.Join(root.inbox, "input.json"), `{}`)
	_, err := Evaluate(context.Background(), Input{
		ArtifactInboxPath: root.inbox,
		Rules: []Rule{{
			ID:        "external_ref",
			Type:      RuleJSONSchema,
			Message:   "No external refs.",
			InboxPath: "input.json",
			Schema:    rawValue(t, `{"$ref":"https://example.com/schema.json"}`),
		}},
	})
	require.ErrorContains(t, err, "unsupported external $ref")
}

func TestChildStatusRule(t *testing.T) {
	output, err := Evaluate(context.Background(), Input{
		Rules: []Rule{
			{
				ID:      "string_status",
				Type:    RuleChildStatus,
				Message: "String status.",
				Status:  rawValue(t, `"completed"`),
			},
			{
				ID:            "object_status",
				Type:          RuleChildStatus,
				Message:       "Object status.",
				Status:        rawValue(t, `{"status":"cancelled"}`),
				AllowStatuses: []string{"completed"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, output.OK)
	require.Equal(t, []string{"object_status"}, output.FailedRuleIDs)
	require.Equal(t, "cancelled", output.Results["object_status"].Details["status"])
}

type roots struct {
	base     string
	inbox    string
	worktree string
}

func testRoots(t *testing.T) roots {
	t.Helper()
	base := t.TempDir()
	root := roots{
		base:     base,
		inbox:    filepath.Join(base, "inbox"),
		worktree: filepath.Join(base, "worktree"),
	}
	require.NoError(t, os.MkdirAll(root.inbox, 0o755))
	require.NoError(t, os.MkdirAll(root.worktree, 0o755))
	return root
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func rawValue(t *testing.T, raw string) JSONValue {
	t.Helper()
	var value JSONValue
	require.NoError(t, json.Unmarshal([]byte(raw), &value))
	return value
}
