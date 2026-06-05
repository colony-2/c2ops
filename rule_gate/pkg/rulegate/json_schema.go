package rulegate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
)

func evaluateJSONSchema(input Input, rule Rule) (bool, RuleResult, error) {
	target, failure, err := parseInboxJSON(input, rule, rule.InboxPath)
	if err != nil {
		return false, RuleResult{}, err
	}
	if failure != nil {
		return false, *failure, nil
	}

	schemaDoc, err := loadSchemaDoc(input, rule)
	if err != nil {
		return false, RuleResult{}, err
	}
	if err := rejectUnsupportedRefs(schemaDoc); err != nil {
		return false, RuleResult{}, err
	}

	compiler := jsonschemav6.NewCompiler()
	compiler.DefaultDraft(jsonschemav6.Draft2020)
	if err := compiler.AddResource("inmem://rule-gate-schema.json", schemaDoc); err != nil {
		return false, RuleResult{}, fmt.Errorf("add schema resource: %w", err)
	}
	compiled, err := compiler.Compile("inmem://rule-gate-schema.json")
	if err != nil {
		return false, RuleResult{}, fmt.Errorf("compile schema: %w", err)
	}
	if err := compiled.Validate(target); err != nil {
		return false, failed(rule, map[string]any{
			"inbox_path": slashPath(rule.InboxPath),
			"error":      err.Error(),
		}), nil
	}
	return true, RuleResult{}, nil
}

func loadSchemaDoc(input Input, rule Rule) (any, error) {
	switch {
	case rule.Schema.Set:
		var doc any
		if err := json.Unmarshal(bytes.TrimSpace(rule.Schema.Raw), &doc); err != nil {
			return nil, fmt.Errorf("parse inline schema: %w", err)
		}
		return doc, nil
	case strings.TrimSpace(rule.SchemaWorktreePath) != "":
		fullPath, err := resolveWorktreePath(input.WorktreePath, rule.SchemaWorktreePath)
		if err != nil {
			return nil, fmt.Errorf("schema_worktree_path: %w", err)
		}
		return readSchemaFile(fullPath, "schema_worktree_path", rule.SchemaWorktreePath)
	case strings.TrimSpace(rule.SchemaInboxPath) != "":
		fullPath, err := resolveInboxPath(input.ArtifactInboxPath, rule.SchemaInboxPath)
		if err != nil {
			return nil, fmt.Errorf("schema_inbox_path: %w", err)
		}
		return readSchemaFile(fullPath, "schema_inbox_path", rule.SchemaInboxPath)
	default:
		return nil, fmt.Errorf("schema source is required")
	}
}

func readSchemaFile(fullPath string, fieldName string, authoredPath string) (any, error) {
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read %s %q: %w", fieldName, slashPath(authoredPath), err)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s %q: %w", fieldName, slashPath(authoredPath), err)
	}
	return doc, nil
}

func rejectUnsupportedRefs(value any) error {
	return walkJSON(value, func(key string, value any) error {
		if key != "$ref" {
			return nil
		}
		ref, ok := value.(string)
		if !ok {
			return fmt.Errorf("$ref must be a string")
		}
		if strings.HasPrefix(ref, "#") {
			return nil
		}
		return fmt.Errorf("unsupported external $ref %q", ref)
	})
}

func walkJSON(value any, visit func(key string, value any) error) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if err := visit(key, child); err != nil {
				return err
			}
			if err := walkJSON(child, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := walkJSON(child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
