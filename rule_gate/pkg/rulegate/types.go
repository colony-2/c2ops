package rulegate

import (
	"encoding/json"
)

const Version = "rule_gate/v1"

type RuleType string

const (
	RuleAssert         RuleType = "assert"
	RuleArtifactExists RuleType = "artifact_exists"
	RuleFileExists     RuleType = "file_exists"
	RuleJSONParse      RuleType = "json_parse"
	RuleJSONSchema     RuleType = "json_schema"
	RuleChildStatus    RuleType = "child_status"
)

const (
	StatusPassed = "passed"
	StatusFailed = "failed"
)

type Input struct {
	ArtifactInboxPath  string `json:"artifact_inbox_path,omitempty"`
	ArtifactOutboxPath string `json:"artifact_outbox_path,omitempty"`
	WorktreePath       string `json:"worktree_path,omitempty"`
	Rules              []Rule `json:"rules"`
}

type Rule struct {
	ID      string   `json:"id"`
	Type    RuleType `json:"type"`
	Message string   `json:"message"`

	Value *bool `json:"value,omitempty"`

	Artifact  JSONValue `json:"artifact,omitempty"`
	Path      string    `json:"path,omitempty"`
	InboxPath string    `json:"inbox_path,omitempty"`

	Schema             JSONValue `json:"schema,omitempty"`
	SchemaWorktreePath string    `json:"schema_worktree_path,omitempty"`
	SchemaInboxPath    string    `json:"schema_inbox_path,omitempty"`

	Status        JSONValue `json:"status,omitempty"`
	AllowStatuses []string  `json:"allow_statuses,omitempty"`
}

type JSONValue struct {
	Set bool
	Raw json.RawMessage
}

func (v *JSONValue) UnmarshalJSON(data []byte) error {
	v.Set = true
	v.Raw = append(v.Raw[:0], data...)
	return nil
}

type Output struct {
	Version       string                `json:"version"`
	OK            bool                  `json:"ok"`
	FailedRuleIDs []string              `json:"failed_rule_ids"`
	Summary       Summary               `json:"summary"`
	Results       map[string]RuleResult `json:"results"`
}

type Summary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

type RuleResult struct {
	ID      string         `json:"id"`
	Type    RuleType       `json:"type"`
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}
