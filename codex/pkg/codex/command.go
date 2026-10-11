package codex

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Keep this package coordinate aligned with both Codex manifests.
const codexPackage = "github:colony-2/c2ops/main#codex-cli"

func codexCommand(args ...string) []string {
	// Packaged ops bind the native CLI's exact store path. Source ops use the
	// qualified runner for their manifest-declared package. Tests can explicitly
	// override the binary without relying on an ambient executable named codex.
	if binary := os.Getenv("C2OPS_CODEX_BIN"); binary != "" {
		return append([]string{binary}, args...)
	}
	return append([]string{"nix", "run", codexPackage, "--"}, args...)
}

func buildCommand(opts Options, schemaContainerPath string) []string {
	cmd := codexCommand(
		"exec",
		"-c", "sqlite_home="+strconv.Quote(opts.CodexHome),
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--output-schema",
		schemaContainerPath,
	)
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
