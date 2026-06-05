package rulegate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func Evaluate(ctx context.Context, input Input) (Output, error) {
	_ = ctx
	if err := Validate(input); err != nil {
		return Output{}, err
	}

	output := Output{
		Version: Version,
		OK:      true,
		Summary: Summary{
			Total: len(input.Rules),
		},
		FailedRuleIDs: []string{},
		Results:       map[string]RuleResult{},
	}

	for _, rule := range input.Rules {
		passed, result, err := evaluateRule(input, rule)
		if err != nil {
			return Output{}, fmt.Errorf("rule %q: %w", rule.ID, err)
		}
		if passed {
			output.Summary.Passed++
			continue
		}
		output.OK = false
		output.Summary.Failed++
		output.FailedRuleIDs = append(output.FailedRuleIDs, rule.ID)
		output.Results[rule.ID] = result
	}

	return output, nil
}

func evaluateRule(input Input, rule Rule) (bool, RuleResult, error) {
	switch rule.Type {
	case RuleAssert:
		return evaluateAssert(rule)
	case RuleArtifactExists:
		return evaluateArtifactExists(rule)
	case RuleFileExists:
		return evaluateFileExists(input, rule)
	case RuleJSONParse:
		return evaluateJSONParse(input, rule)
	case RuleJSONSchema:
		return evaluateJSONSchema(input, rule)
	case RuleChildStatus:
		return evaluateChildStatus(rule)
	default:
		return false, RuleResult{}, fmt.Errorf("unknown rule type %q", rule.Type)
	}
}

func evaluateAssert(rule Rule) (bool, RuleResult, error) {
	if *rule.Value {
		return true, RuleResult{}, nil
	}
	return false, failed(rule, map[string]any{"value": false}), nil
}

func evaluateArtifactExists(rule Rule) (bool, RuleResult, error) {
	present := artifactPresent(rule.Artifact)
	if present {
		return true, RuleResult{}, nil
	}
	return false, failed(rule, map[string]any{"artifact_present": false}), nil
}

func artifactPresent(value JSONValue) bool {
	raw := bytes.TrimSpace(value.Raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return false
		}
		return strings.TrimSpace(s) != ""
	}
	if raw[0] == '{' {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return false
		}
		return len(obj) > 0
	}
	return false
}

func evaluateFileExists(input Input, rule Rule) (bool, RuleResult, error) {
	fullPath, err := resolveWorktreePath(input.WorktreePath, rule.Path)
	if err != nil {
		return false, RuleResult{}, err
	}
	info, err := os.Stat(fullPath)
	if err == nil {
		if info.IsDir() {
			return false, failed(rule, map[string]any{
				"path":  slashPath(rule.Path),
				"error": "path is a directory",
			}), nil
		}
		return true, RuleResult{}, nil
	}
	if os.IsNotExist(err) {
		return false, failed(rule, map[string]any{
			"path":  slashPath(rule.Path),
			"error": "file does not exist",
		}), nil
	}
	return false, RuleResult{}, fmt.Errorf("stat path %q: %w", slashPath(rule.Path), err)
}

func evaluateJSONParse(input Input, rule Rule) (bool, RuleResult, error) {
	_, failure, err := parseInboxJSON(input, rule, rule.InboxPath)
	if err != nil {
		return false, RuleResult{}, err
	}
	if failure != nil {
		return false, *failure, nil
	}
	return true, RuleResult{}, nil
}

func parseInboxJSON(input Input, rule Rule, inboxPath string) (any, *RuleResult, error) {
	fullPath, err := resolveInboxPath(input.ArtifactInboxPath, inboxPath)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			result := failed(rule, map[string]any{
				"inbox_path": slashPath(inboxPath),
				"error":      "file does not exist",
			})
			return nil, &result, nil
		}
		return nil, nil, fmt.Errorf("read inbox file %q: %w", slashPath(inboxPath), err)
	}
	var parsed any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		result := failed(rule, map[string]any{
			"inbox_path": slashPath(inboxPath),
			"error":      err.Error(),
		})
		return nil, &result, nil
	}
	return parsed, nil, nil
}

func evaluateChildStatus(rule Rule) (bool, RuleResult, error) {
	status, ok := extractStatus(rule.Status)
	if !ok {
		return false, failed(rule, map[string]any{"error": "status could not be extracted"}), nil
	}
	allowed := rule.AllowStatuses
	if len(allowed) == 0 {
		allowed = []string{"completed"}
	}
	for _, candidate := range allowed {
		if status == candidate {
			return true, RuleResult{}, nil
		}
	}
	return false, failed(rule, map[string]any{
		"status":         status,
		"allow_statuses": allowed,
	}), nil
}

func extractStatus(value JSONValue) (string, bool) {
	raw := bytes.TrimSpace(value.Raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	if raw[0] == '"' {
		var status string
		if err := json.Unmarshal(raw, &status); err != nil {
			return "", false
		}
		return status, status != ""
	}
	if raw[0] == '{' {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return "", false
		}
		status, ok := obj["status"].(string)
		return status, ok && status != ""
	}
	return "", false
}

func failed(rule Rule, details map[string]any) RuleResult {
	return RuleResult{
		ID:      rule.ID,
		Type:    rule.Type,
		Status:  StatusFailed,
		Message: rule.Message,
		Details: details,
	}
}
