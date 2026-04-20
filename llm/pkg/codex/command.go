package codex

import (
	"fmt"
	"strings"
)

func buildCommand(opts Options, schemaContainerPath string) []string {
	cmd := []string{
		"codex",
		"exec",
		"--experimental-json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--output-schema",
		schemaContainerPath,
	}
	if strings.TrimSpace(opts.Model) != "" {
		cmd = append(cmd, "--model", opts.Model)
	}
	if strings.TrimSpace(opts.SessionID) != "" {
		cmd = append(cmd, "resume", opts.SessionID)
	}
	cmd = append(cmd, opts.Prompt)
	return cmd
}

func buildEnv(opts Options) map[string]string {
	env := map[string]string{}
	for k, v := range opts.copyEnv() {
		env[k] = v
	}
	if _, ok := env["CODEX_APPROVAL_POLICY"]; !ok {
		env["CODEX_APPROVAL_POLICY"] = "never"
	}
	if strings.TrimSpace(opts.CodexHome) != "" {
		env["CODEX_HOME"] = opts.CodexHome
	}
	return env
}

func joinBlobURI(baseURI, relative string) string {
	if strings.HasSuffix(baseURI, "/") {
		return baseURI + relative
	}
	return fmt.Sprintf("%s/%s", baseURI, relative)
}
