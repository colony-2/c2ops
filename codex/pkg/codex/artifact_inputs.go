package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ArtifactFileInput is c2j's transient subprocess handoff for a direct artifact
// reference. Recipe authors supply only the reference; c2j resolves and stages it.
// Ref remains opaque here because reference validation belongs to the runtime.
type ArtifactFileInput struct {
	Ref  json.RawMessage `json:"ref"`
	Path string          `json:"path"`
}

func (file ArtifactFileInput) validate(field string) error {
	var ref map[string]json.RawMessage
	if json.Unmarshal(file.Ref, &ref) != nil || len(ref) == 0 || strings.TrimSpace(file.Path) == "" {
		return fmt.Errorf("%s: expected a runtime-resolved artifact reference with ref and path; upgrade c2j to a version supporting x-c2j-artifact-file", field)
	}
	return nil
}

// readArtifactText reads c2j's private staged file during this invocation.
// The runtime owns resolution and cleanup; the operation owns text validation.
func readArtifactText(field, path, workdir string) (string, error) {
	fail := func(reason string) (string, error) { return "", fmt.Errorf("%s: %s", field, reason) }
	if strings.TrimSpace(path) == "" {
		return fail("path is required")
	}
	if !filepath.IsAbs(path) {
		return fail("path must be absolute and inside the invocation directory")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return fail("path traversal is not allowed")
		}
	}
	base, err := filepath.Abs(workdir)
	if err != nil {
		return fail("cannot resolve invocation directory")
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(rel) {
		return fail("path must be inside the invocation directory")
	}
	// Resolve aliases on both paths so contained absolute symlinks work too.
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return fail("cannot resolve invocation directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fail("cannot resolve staged file")
	}
	rel, err = filepath.Rel(base, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return fail("staged file resolves outside the invocation directory")
	}
	// Root also confines the subsequent access if a resolved path component
	// changes to a symlink between resolution and opening the file.
	root, err := os.OpenRoot(base)
	if err != nil {
		return fail("cannot open invocation directory")
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		return fail("cannot access staged file inside the invocation directory")
	}
	if !info.Mode().IsRegular() {
		return fail("staged file must be a regular file")
	}
	file, err := root.Open(rel)
	if err != nil {
		return fail("cannot open staged file inside the invocation directory")
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fail("staged file must be a regular file")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return fail("cannot read staged file")
	}
	if !utf8.Valid(data) {
		return fail("staged file must contain valid UTF-8")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return fail("staged file must not contain NUL bytes")
	}
	return string(data), nil
}

// Resolve before session preparation, including on resume, so ignored
// instructions still have to be available and valid for every invocation.
func resolveArtifactTextInputs(input *ExecOpInput, workdir string) error {
	if strings.TrimSpace(input.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	if input.InstructionsFile != nil {
		if strings.TrimSpace(input.Instructions) != "" {
			return fmt.Errorf("instructions_file cannot be combined with nonblank instructions")
		}
		if err := input.InstructionsFile.validate("instructions_file"); err != nil {
			return err
		}
		text, err := readArtifactText("instructions_file", input.InstructionsFile.Path, workdir)
		if err != nil {
			return err
		}
		input.Instructions = text
	}
	if input.PromptPrefixFile != nil {
		if err := input.PromptPrefixFile.validate("prompt_prefix_file"); err != nil {
			return err
		}
		text, err := readArtifactText("prompt_prefix_file", input.PromptPrefixFile.Path, workdir)
		if err != nil {
			return err
		}
		input.promptPrefix = text
	}
	return nil
}

func composePrompt(prefix, task string) string {
	if prefix == "" {
		return task
	}
	return prefix + "\n\n" + task
}
