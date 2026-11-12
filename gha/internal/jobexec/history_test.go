package jobexec

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func historyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}
func historyCommit(t *testing.T, dir, name string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0600))
	historyGit(t, dir, "add", ".")
	return historyGit(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-m", name)
}

type historyFixture struct{ remote, source, old, candidate, tree string }

func newHistoryFixture(t *testing.T, shape string) historyFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "published")
	require.NoError(t, os.Mkdir(remote, 0700))
	historyGit(t, remote, "init", "-b", "main")
	historyCommit(t, remote, "old.txt")
	old := historyGit(t, remote, "rev-parse", "HEAD")
	historyGit(t, remote, "tag", "legacy")
	historyGit(t, remote, "branch", "historical")
	historyCommit(t, remote, "base.txt")
	source := filepath.Join(root, "candidate")
	args := []string{"clone", "--no-local"}
	if shape == "shallow" {
		args = append(args, "--depth=1", "--no-tags")
	}
	if shape == "incomplete-refs" {
		args = append(args, "--single-branch", "--no-tags")
	}
	if shape == "alternate" {
		args = []string{"clone", "--shared"}
	}
	args = append(args, "file://"+remote, source)
	historyGit(t, root, args...)
	if shape == "linked" {
		linked := filepath.Join(root, "linked")
		historyGit(t, source, "worktree", "add", "--detach", linked)
		source = linked
	}
	historyCommit(t, source, "candidate.txt")
	candidate := historyGit(t, source, "rev-parse", "HEAD")
	tree := historyGit(t, source, "rev-parse", "HEAD^{tree}")
	// The source remote deliberately never contains the candidate.
	cmd := exec.Command("git", "-C", remote, "cat-file", "-e", candidate)
	require.Error(t, cmd.Run())
	return historyFixture{remote, source, old, candidate, tree}
}
func TestHistoryIndependentFrozenCandidate(t *testing.T) {
	for _, shape := range []string{"complete", "shallow", "incomplete-refs", "linked", "alternate"} {
		t.Run(shape, func(t *testing.T) {
			f := newHistoryFixture(t, shape)
			state := t.TempDir()
			in := Request{Repository: "file://" + f.remote, Revision: f.candidate}
			repo, err := prepareHistory(t.Context(), f.source, state, in)
			require.NoError(t, err)
			// Source stores and remote disappear before the final act execution copy.
			require.NoError(t, os.RemoveAll(filepath.Dir(f.remote)))
			check := fmt.Sprintf(`test "$(git rev-parse HEAD)" = %s
          test "$(git rev-parse 'HEAD^{tree}')" = %s
          git archive %s old.txt | tar -xO | grep old.txt
          git cat-file -e refs/tags/legacy
          git cat-file -e refs/remotes/c2j-source/historical
          test "$(git rev-parse --is-shallow-repository)" = false
          test ! -e .git/objects/info/alternates
          git fsck --full`, f.candidate, f.tree, f.old)
			var logs bytes.Buffer
			result, err := Execute(t.Context(), Request{Revision: f.candidate, JobID: "probe", Workflow: "on: workflow_dispatch\njobs:\n  probe:\n    runs-on: local\n    steps:\n      - run: |\n          " + check + "\n"}, Options{Workdir: repo, StateDir: t.TempDir(), Log: &logs})
			require.NoError(t, err, logs.String())
			require.Equal(t, "success", result.Conclusion, logs.String())
		})
	}
}
func TestHistoryExecuteFetchDepth(t *testing.T) {
	f := newHistoryFixture(t, "shallow")
	wf := fmt.Sprintf(`on: workflow_dispatch
jobs:
  probe:
    runs-on: local
    steps:
      - uses: actions/checkout@v4
        with: {fetch-depth: 0}
      - run: |
          test "$(git rev-parse HEAD)" = %s
          test "$(git rev-parse 'HEAD^{tree}')" = %s
          git archive %s old.txt | tar -xO | grep old.txt
`, f.candidate, f.tree, f.old)
	var logs bytes.Buffer
	result, err := Execute(t.Context(), Request{Repository: "file://" + f.remote, Revision: f.candidate, Workflow: wf, JobID: "probe"}, Options{Workdir: f.source, StateDir: t.TempDir(), Log: &logs})
	require.NoError(t, err, logs.String())
	require.Equal(t, "success", result.Conclusion, logs.String())
	require.Equal(t, "true", historyGit(t, f.source, "rev-parse", "--is-shallow-repository"), "managed source was not changed")
}
func TestCheckoutOptionsAgreeBeforeCommands(t *testing.T) {
	markerRoot := t.TempDir()
	for _, options := range []string{"", "{fetch-depth: 0}", "{fetch-depth: '0'}", "{fetch-depth: 1}", "{fetch-depth: '${{ github.run_number }}'}", "{ref: main}", "{fetch-depth: 0, path: other}", "{fetch-depth: false}", "{fetch-depth: 00}"} {
		t.Run(options, func(t *testing.T) {
			line := ""
			if options != "" {
				line = "        with: " + options + "\n"
			}
			dir := t.TempDir()
			marker := filepath.Join(markerRoot, "ran")
			os.Remove(marker)
			wf := "on: workflow_dispatch\njobs:\n  probe:\n    runs-on: local\n    steps:\n      - uses: actions/checkout@v4\n" + line + "      - run: touch " + marker + "\n"
			valid := options == "" || options == "{fetch-depth: 0}" || options == "{fetch-depth: '0'}"
			err := Validate(wf, "probe", dir)
			if valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "literal fetch-depth")
			}
			_, err = Execute(t.Context(), Request{Workflow: wf, JobID: "probe"}, Options{Workdir: dir, StateDir: t.TempDir()})
			if !valid {
				require.ErrorContains(t, err, "literal fetch-depth")
				require.NoFileExists(t, marker)
			} else if options == "" {
				require.NoError(t, err)
				require.FileExists(t, marker)
			}
		})
	}
}
func TestHistoryFailuresPrecedeCommands(t *testing.T) {
	for _, mode := range []string{"unavailable", "missing-credential", "tag-conflict", "dirty", "drift", "missing-object"} {
		t.Run(mode, func(t *testing.T) {
			f := newHistoryFixture(t, "shallow")
			marker := filepath.Join(t.TempDir(), "ran")
			in := Request{Repository: "file://" + f.remote, Revision: f.candidate, JobID: "probe", Workflow: "on: workflow_dispatch\njobs:\n  probe:\n    runs-on: local\n    steps:\n      - uses: actions/checkout@v4\n        with: {fetch-depth: 0}\n      - run: touch " + marker + "\n"}
			switch mode {
			case "unavailable":
				require.NoError(t, os.RemoveAll(f.remote))
			case "missing-credential":
				in.SourceCredential = "MISSING"
			case "tag-conflict":
				historyGit(t, f.source, "tag", "legacy", f.candidate)
			case "dirty":
				require.NoError(t, os.WriteFile(filepath.Join(f.source, "dirty"), []byte("dirty"), 0600))
			case "drift":
				in.Revision = f.old
			case "missing-object":
				require.NoError(t, os.Remove(filepath.Join(f.source, ".git", "objects", f.candidate[:2], f.candidate[2:])))
			}
			_, err := Execute(t.Context(), in, Options{Workdir: f.source, StateDir: t.TempDir()})
			require.Error(t, err)
			require.NoFileExists(t, marker)
			t.Log(mode, err)
		})
	}
}

func TestHistoryScopedCredentials(t *testing.T) {
	f := newHistoryFixture(t, "complete")
	historyGit(t, f.remote, "update-server-info")
	secret := "preparation-private-password"
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		user, pass, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || pass != secret {
			w.Header().Set("WWW-Authenticate", `Basic realm="source"`)
			w.WriteHeader(401)
			fmt.Fprint(w, secret)
			return
		}
		http.FileServer(http.Dir(filepath.Join(f.remote, ".git"))).ServeHTTP(w, r)
	}))
	defer server.Close()
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			value := "wrong-private-password"
			if valid {
				value = secret
			}
			repo, err := prepareHistory(context.Background(), f.source, t.TempDir(), Request{Repository: server.URL, SourceCredential: "SOURCE", Secrets: map[string]string{"SOURCE": value}})
			if !valid {
				require.Error(t, err)
				require.NotContains(t, err.Error(), secret)
				require.NotContains(t, err.Error(), value)
				return
			}
			require.NoError(t, err)
			require.NotContains(t, historyGit(t, repo, "config", "--list", "--local"), secret)
			require.NoError(t, filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
				require.NoError(t, err)
				if !d.IsDir() {
					raw, e := os.ReadFile(path)
					require.NoError(t, e)
					require.NotContains(t, string(raw), secret, path)
				}
				return nil
			}))
		})
	}
	require.Positive(t, requests)
}

func TestPreparationCredentialScope(t *testing.T) {
	source := "https://source.example.test/team/source.git"
	u, err := url.Parse(source)
	require.NoError(t, err)
	env, err := sourceCredentials(t.TempDir(), Request{Repository: source, SourceCredential: "SOURCE", Secrets: map[string]string{"SOURCE": "scoped-private-token"}}, u)
	require.NoError(t, err)
	for _, scope := range []struct {
		host, path string
		allowed    bool
	}{{"source.example.test", "team/source.git", true}, {"other.example.test", "team/source.git", false}, {"source.example.test", "team/other.git", false}, {"source.example.test", "team/source.git/other", false}} {
		cmd := exec.Command("git", "credential", "fill")
		cmd.Env = append(os.Environ(), env...)
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + scope.host + "\npath=" + scope.path + "\n\n")
		out, err := cmd.Output()
		if scope.allowed {
			require.NoError(t, err)
			require.Contains(t, string(out), "password=scoped-private-token")
		} else {
			require.Error(t, err)
			require.NotContains(t, string(out), "scoped-private-token")
		}
	}
	for _, source := range []string{"github.com/team/repo", "git@github.com:team/repo.git", "ssh://git@github.com/team/repo.git", "https://github.com/team/repo.git"} {
		require.NoError(t, ValidateSourceRepository(source))
	}
	for _, source := range []string{"https://token@github.com/team/repo", "https://user:password@github.com/team/repo", "ssh://git:password@github.com/team/repo"} {
		require.Error(t, ValidateSourceRepository(source))
	}
}
