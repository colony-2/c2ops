package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
)

func optionalSchemaFile(schema []byte) (string, func(), error) {
	if len(schema) == 0 {
		return "", func() {}, nil
	}
	return writeSchemaTempFile(schema)
}

// Execute requests the caller's schema directly, then independently validates
// the final response. Repairs resume the same session and retain every event.
func Execute(ctx context.Context, opts Options) (Result, string, string, string, error) {
	schema := bytes.TrimSpace(opts.StructuredSchema)
	if bytes.Equal(schema, []byte("null")) {
		schema = nil
	}
	compiled, err := compileOutputSchema(schema)
	if err != nil {
		return Result{}, "", "", "", err
	}
	maxRepairs := 3
	if opts.MaxRepairAttempts != nil {
		maxRepairs = *opts.MaxRepairAttempts
	}
	if maxRepairs < 0 {
		return Result{}, "", "", "", fmt.Errorf("output.max_repair_attempts must not be negative")
	}
	opts.StructuredSchema = schema
	if compiled == nil {
		return executeOnce(ctx, opts)
	}

	dir, err := os.MkdirTemp("", "codex-validated-*")
	if err != nil {
		return Result{}, "", "", "", err
	}
	stdout, stderr := filepath.Join(dir, "stdout.jsonl"), filepath.Join(dir, "stderr.txt")
	// Keep valid artifact paths even if validation/setup fails before CLI launch.
	for _, path := range []string{stdout, stderr} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			_ = os.RemoveAll(dir)
			return Result{}, "", "", "", err
		}
	}
	var events, items []json.RawMessage
	originalPrompt := opts.Prompt
	mode := "native"
	for attempt := 0; ; attempt++ {
		result, attemptStdout, attemptStderr, attemptDir, runErr := executeOnce(ctx, opts)
		events = append(events, result.Events...)
		items = append(items, result.Items...)
		result.Events, result.Items = events, items
		result.OutputRepairAttempts = attempt
		result.SchemaMode = mode
		// A schema-constrained response is published only after successful validation.
		result.Response = nil
		valid := false
		result.OutputSchemaValid = &valid
		copyErr := appendExecutionLogs(stdout, stderr, attemptStdout, attemptStderr)
		cleanupArtifacts(attemptStdout, attemptStderr, attemptDir)
		if copyErr != nil {
			return result, stdout, stderr, dir, copyErr
		}
		if runErr != nil {
			return result, stdout, stderr, dir, runErr
		}
		if err := ctx.Err(); err != nil {
			return result, stdout, stderr, dir, err
		}
		if opts.SessionID != "" && result.SessionID != "" && opts.SessionID != result.SessionID {
			return result, stdout, stderr, dir, fmt.Errorf("Codex resumed a different session")
		}
		nativeRejected := result.Status == StatusError && mode == "native" && nativeSchemaRejected(result)
		if result.Status != StatusCompleted && !nativeRejected {
			return result, stdout, stderr, dir, nil
		}
		var validationErr error
		if nativeRejected {
			validationErr = fmt.Errorf("provider rejected native structured output: %s", result.ErrorMessage)
		} else {
			var value any
			value, validationErr = jsonschemav6.UnmarshalJSON(strings.NewReader(result.ResponseText))
			if validationErr == nil {
				validationErr = compiled.Validate(value)
			}
			if validationErr == nil {
				valid = true
				result.Response = json.RawMessage(strings.TrimSpace(result.ResponseText))
				return result, stdout, stderr, dir, nil
			}
		}
		result.OutputSchemaErrors = []string{validationErr.Error()}
		if attempt >= maxRepairs || result.SessionID == "" {
			result.Status = StatusError
			result.ErrorMessage = fmt.Sprintf("output schema validation failed after %d repair attempts: %v", attempt, validationErr)
			return result, stdout, stderr, dir, nil
		}
		opts.SessionID = result.SessionID
		if nativeRejected {
			// Only explicit schema rejection permits prompt-only fallback; never retry
			// authentication errors, failed commands, cancellation, or arbitrary failures.
			mode = "prompt"
			opts.StructuredSchema = nil
			opts.Prompt = originalPrompt + "\n\nNative structured output is unavailable."
		} else {
			opts.Prompt = "Correct your previous final response to satisfy the requested JSON schema. Preserve the answer's meaning; do not repeat completed work or side effects."
		}
		opts.Prompt += "\nReturn only a JSON value matching this schema:\n" + string(schema) + "\nValidation feedback:\n" + validationErr.Error()
		progress(ctx, "output.repair", map[string]any{"attempt": attempt + 1, "schema_mode": mode})
	}
}

func nativeSchemaRejected(result Result) bool {
	if result.ResponseText != "" {
		return false
	}
	// A rejected format fails before a response/tool is produced. Do not replay
	// an invocation that has already executed tools.
	for _, raw := range result.Items {
		var item eventItem
		if json.Unmarshal(raw, &item) == nil && item.Type != "reasoning" && item.Type != "error" {
			return false
		}
	}
	message := strings.ToLower(result.ErrorMessage)
	return strings.Contains(message, "invalid_json_schema") ||
		strings.Contains(message, "unsupported_response_format") ||
		((strings.Contains(message, "json_schema") || strings.Contains(message, "output schema") || strings.Contains(message, "response_format")) &&
			(strings.Contains(message, "not supported") || strings.Contains(message, "unsupported") || strings.Contains(message, "invalid schema")))
}

func appendExecutionLogs(stdout, stderr, sourceStdout, sourceStderr string) error {
	for _, pair := range [][2]string{{stdout, sourceStdout}, {stderr, sourceStderr}} {
		if pair[1] == "" {
			continue
		}
		src, err := os.Open(pair[1])
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(pair[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			src.Close()
			return err
		}
		_, copyErr := io.Copy(dst, src)
		src.Close()
		closeErr := dst.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func compileOutputSchema(raw json.RawMessage) (*jsonschemav6.Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	doc, err := jsonschemav6.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("output schema is not valid JSON: %w", err)
	}
	compiler := jsonschemav6.NewCompiler()
	compiler.DefaultDraft(jsonschemav6.Draft2020)
	if err := compiler.AddResource("inmem://codex-output-schema.json", doc); err != nil {
		return nil, fmt.Errorf("add output schema resource: %w", err)
	}
	compiled, err := compiler.Compile("inmem://codex-output-schema.json")
	if err != nil {
		return nil, fmt.Errorf("compile output schema: %w", err)
	}
	return compiled, nil
}
