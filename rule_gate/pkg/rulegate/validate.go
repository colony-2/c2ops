package rulegate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func Validate(input Input) error {
	if len(input.Rules) == 0 {
		return fmt.Errorf("rules must contain at least one rule")
	}
	seen := map[string]struct{}{}
	for i, rule := range input.Rules {
		if strings.TrimSpace(rule.ID) == "" {
			return fmt.Errorf("rules[%d].id is required", i)
		}
		if _, ok := seen[rule.ID]; ok {
			return fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		if strings.TrimSpace(rule.Message) == "" {
			return fmt.Errorf("rule %q message is required", rule.ID)
		}
		if err := validateRule(input, rule); err != nil {
			return fmt.Errorf("rule %q: %w", rule.ID, err)
		}
	}
	return nil
}

func validateRule(input Input, rule Rule) error {
	switch rule.Type {
	case RuleAssert:
		if rule.Value == nil {
			return fmt.Errorf("value is required")
		}
	case RuleArtifactExists:
		if !rule.Artifact.Set {
			return fmt.Errorf("artifact is required")
		}
		if err := validateArtifactValue(rule.Artifact); err != nil {
			return err
		}
	case RuleFileExists:
		if _, err := resolveWorktreePath(input.WorktreePath, rule.Path); err != nil {
			return err
		}
	case RuleJSONParse:
		if _, err := resolveInboxPath(input.ArtifactInboxPath, rule.InboxPath); err != nil {
			return err
		}
	case RuleJSONSchema:
		if _, err := resolveInboxPath(input.ArtifactInboxPath, rule.InboxPath); err != nil {
			return err
		}
		if err := validateSchemaSources(input, rule); err != nil {
			return err
		}
	case RuleChildStatus:
		if !rule.Status.Set {
			return fmt.Errorf("status is required")
		}
		for _, status := range rule.AllowStatuses {
			if strings.TrimSpace(status) == "" {
				return fmt.Errorf("allow_statuses must not contain empty values")
			}
		}
	default:
		return fmt.Errorf("unknown rule type %q", rule.Type)
	}
	return nil
}

func validateArtifactValue(value JSONValue) error {
	raw := bytes.TrimSpace(value.Raw)
	if len(raw) == 0 {
		return fmt.Errorf("artifact is required")
	}
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("artifact string is invalid JSON: %w", err)
		}
		return nil
	case '{':
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return fmt.Errorf("artifact object is invalid JSON: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("artifact must be an object, string, null, or empty string")
	}
}

func validateSchemaSources(input Input, rule Rule) error {
	count := 0
	if rule.Schema.Set {
		count++
		if err := validateInlineSchema(rule.Schema); err != nil {
			return err
		}
	}
	if strings.TrimSpace(rule.SchemaWorktreePath) != "" {
		count++
		if _, err := resolveWorktreePath(input.WorktreePath, rule.SchemaWorktreePath); err != nil {
			return fmt.Errorf("schema_worktree_path: %w", err)
		}
	}
	if strings.TrimSpace(rule.SchemaInboxPath) != "" {
		count++
		if _, err := resolveInboxPath(input.ArtifactInboxPath, rule.SchemaInboxPath); err != nil {
			return fmt.Errorf("schema_inbox_path: %w", err)
		}
	}
	if count != 1 {
		return fmt.Errorf("exactly one of schema, schema_worktree_path, or schema_inbox_path is required")
	}
	return nil
}

func validateInlineSchema(value JSONValue) error {
	raw := bytes.TrimSpace(value.Raw)
	if len(raw) == 0 || raw[0] != '{' {
		return fmt.Errorf("schema must be an object")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("schema is invalid JSON: %w", err)
	}
	return nil
}
