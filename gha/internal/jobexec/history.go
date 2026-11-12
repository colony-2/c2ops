package jobexec

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nektos/act/pkg/model"
)

// checkoutHistory deliberately implements only the frozen-source checkout
// contract. In particular a ref or expression must never select another source.
func checkoutHistory(step *model.Step) (bool, error) {
	if !strings.HasPrefix(step.Uses, "actions/checkout@") {
		return false, nil
	}
	if len(step.With) == 0 {
		return false, nil
	}
	if len(step.With) == 1 && step.With["fetch-depth"] == "0" {
		return true, nil
	}
	return false, fmt.Errorf("checkout options require only literal fetch-depth: 0 with a frozen source snapshot")
}

var sshSource = regexp.MustCompile(`^git@[A-Za-z0-9_.-]+:[A-Za-z0-9_./-]+$`)

// ValidateSourceRepository permits public source locators, including the
// conventional SSH Git username, but never token-bearing URL userinfo.
func ValidateSourceRepository(repository string) error {
	if sshSource.MatchString(repository) {
		return nil
	}
	u, err := url.Parse(repository)
	if err != nil {
		return fmt.Errorf("invalid source repository locator")
	}
	if u.User != nil {
		_, password := u.User.Password()
		if u.Scheme != "ssh" || u.User.Username() != "git" || password {
			return fmt.Errorf("source repository must not contain URL credentials")
		}
	}
	if strings.HasPrefix(repository, "-") {
		return fmt.Errorf("invalid source repository locator")
	}
	return nil
}

// gitOutput never includes Git's stderr in errors: transports and external
// credential helpers can echo secrets, including values not known to this op.
// The operation/stage and exit status remain actionable without that output.
func gitOutput(ctx context.Context, root string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("source history Git %s failed: %w (check source access and object completeness)", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

func prepareHistory(ctx context.Context, source, state string, in Request) (string, error) {
	if in.Repository == "" {
		return "", fmt.Errorf("full history requires the authorized source repository")
	}
	// c2j registry identities use host/owner/repo. Accept that public identity
	// as well as explicit Git transports and local deterministic fixtures.
	repository := in.Repository
	if !strings.Contains(repository, ":") && !filepath.IsAbs(repository) {
		host, _, found := strings.Cut(repository, "/")
		if found && strings.Contains(host, ".") {
			repository = "https://" + strings.TrimSuffix(repository, ".git") + ".git"
		}
	}
	in.Repository = repository
	if err := ValidateSourceRepository(in.Repository); err != nil {
		return "", err
	}
	u, _ := url.Parse(in.Repository) // nil for conventional scp-style SSH syntax
	dirty, err := gitOutput(ctx, source, nil, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if dirty != "" {
		return "", fmt.Errorf("full-history source must be a clean frozen snapshot")
	}
	head, err := gitOutput(ctx, source, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	tree, err := gitOutput(ctx, source, nil, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	repo := filepath.Join(state, "source")
	if err := os.Mkdir(repo, 0700); err != nil {
		return "", err
	}
	// Git's transport resolves linked-worktree gitdirs and alternates and copies
	// reachable objects into owned packs. --no-local prevents hardlinks and the
	// local clone shortcut (which otherwise copies alternates verbatim).
	if _, err := gitOutput(ctx, state, nil, "clone", "--mirror", "--no-local", "--", source, filepath.Join(repo, ".git")); err != nil {
		return "", err
	}
	gitdir := filepath.Join(repo, ".git")
	if _, err := gitOutput(ctx, gitdir, nil, "config", "core.bare", "false"); err != nil {
		return "", err
	}
	if _, err := gitOutput(ctx, repo, nil, "remote", "remove", "origin"); err != nil {
		return "", err
	}
	// A detached candidate is not necessarily referenced by any branch. Hold it
	// before fetching so published refs can never replace or prune it.
	if _, err := gitOutput(ctx, repo, nil, "update-ref", "refs/c2j/candidate", head); err != nil {
		return "", err
	}
	env, err := sourceCredentials(state, in, u)
	if err != nil {
		return "", err
	}
	shallow, err := gitOutput(ctx, repo, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return "", err
	}
	args := []string{"-c", "http.followRedirects=false", "fetch", "--no-write-fetch-head", "--no-recurse-submodules"}
	if shallow == "true" {
		args = append(args, "--unshallow")
	}
	// Fetch advertised published refs, never HEAD/the unpublished candidate hash.
	// Tags deliberately have no force prefix: conflicts fail rather than rewrite.
	args = append(args, "--", in.Repository, "+refs/heads/*:refs/remotes/c2j-source/*", "refs/tags/*:refs/tags/*")
	if _, err := gitOutput(ctx, repo, env, args...); err != nil {
		return "", fmt.Errorf("obtain authorized branch/tag history: %w", err)
	}
	shallow, err = gitOutput(ctx, repo, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return "", err
	}
	if shallow != "false" {
		return "", fmt.Errorf("source history remains shallow after authorized fetch")
	}
	if _, err := os.Stat(filepath.Join(gitdir, "objects", "info", "alternates")); !os.IsNotExist(err) {
		return "", fmt.Errorf("source history retains an external object store")
	}
	if _, err := gitOutput(ctx, repo, nil, "fsck", "--full", "--no-reflogs"); err != nil {
		return "", err
	}
	if _, err := gitOutput(ctx, repo, nil, "checkout", "--detach", head); err != nil {
		return "", err
	}
	actual, err := gitOutput(ctx, repo, nil, "rev-parse", "HEAD", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	if actual != head+"\n"+tree {
		return "", fmt.Errorf("candidate HEAD/tree changed during history preparation")
	}
	return repo, nil
}

// A credential helper containing no credential bytes lives only in disposable
// state, outside the repository. Git scopes it to the exact HTTP host/path;
// redirects and all other credential helpers are disabled for this fetch.
func sourceCredentials(state string, in Request, u *url.URL) ([]string, error) {
	if in.SourceCredential == "" {
		return nil, nil
	}
	secret, ok := in.Secrets[strings.ToUpper(in.SourceCredential)]
	if !ok || secret == "" {
		return nil, fmt.Errorf("source_credential has no resolved secret; bind and grant its named secret explicitly")
	}
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("source_credential requires an HTTP(S) repository URL without query or fragment")
	}
	helper := filepath.Join(state, "source-credential-helper")
	script := `#!/bin/sh
[ "$1" = get ] || exit 0
protocol= host= path=
while IFS='=' read -r key value; do
 case "$key" in protocol) protocol=$value;; host) host=$value;; path) path=$value;; esac
done
[ "$protocol" = "$GHA_SOURCE_PROTOCOL" ] && [ "$host" = "$GHA_SOURCE_HOST" ] && [ "$path" = "$GHA_SOURCE_PATH" ] || exit 0
printf 'username=x-access-token\npassword=%s\n' "$GHA_SOURCE_PASSWORD"
`
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		return nil, err
	}
	return []string{
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_PARAMETERS=",
		"GIT_TRACE=0", "GIT_TRACE_CURL=0", "GIT_CURL_VERBOSE=0", "GIT_TRACE_PACKET=0", "GIT_TRACE2=0",
		"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.useHttpPath", "GIT_CONFIG_VALUE_1=true",
		"GIT_CONFIG_KEY_2=credential." + in.Repository + ".helper", "GIT_CONFIG_VALUE_2=" + helper,
		"GHA_SOURCE_PROTOCOL=" + u.Scheme, "GHA_SOURCE_HOST=" + u.Host, "GHA_SOURCE_PATH=" + strings.TrimPrefix(u.Path, "/"), "GHA_SOURCE_PASSWORD=" + secret,
		"GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false",
	}, nil
}
