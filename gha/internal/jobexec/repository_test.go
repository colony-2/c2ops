package jobexec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepositoryContextAcrossHistoryPreparation(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, conditional := range []bool{false, true} {
			t.Run(fmt.Sprintf("history=%t/condition=%t", full, conditional), func(t *testing.T) {
				f := newHistoryFixture(t, "shallow")
				historyGit(t, f.source, "remote", "set-url", "origin", "https://github.com/colony-2/c2j.git")
				checkout, condition := "", ""
				if full {
					checkout = "        with: {fetch-depth: 0}\n"
				}
				if conditional {
					condition = "    if: github.event_name == 'pull_request' && github.base_ref == 'main' && github.repository == 'colony-2/c2j' && github.repository_owner == 'colony-2'\n"
				}
				workflow := "on: pull_request\njobs:\n  probe:\n" + condition + `    runs-on: ['${{ github.repository_owner }}']
    outputs:
      repository: ${{ steps.check.outputs.repository }}
      owner: ${{ steps.check.outputs.owner }}
      head: ${{ steps.check.outputs.head }}
      tree: ${{ steps.check.outputs.tree }}
    steps:
      - uses: actions/checkout@v4
` + checkout + `      - id: check
        if: github.repository == 'colony-2/c2j' && github.repository_owner == 'colony-2'
        env:
          REPOSITORY: ${{ github.repository }}
          OWNER: ${{ github.repository_owner }}
        run: |
          test "$REPOSITORY:$GITHUB_REPOSITORY" = colony-2/c2j:colony-2/c2j
          test "$OWNER:$GITHUB_REPOSITORY_OWNER" = colony-2:colony-2
          test "$GITHUB_EVENT_NAME:$GITHUB_BASE_REF:$GITHUB_HEAD_REF" = pull_request:main:topic
          echo "repository=$GITHUB_REPOSITORY" >> "$GITHUB_OUTPUT"
          echo "owner=$GITHUB_REPOSITORY_OWNER" >> "$GITHUB_OUTPUT"
          echo "head=$(git rev-parse HEAD)" >> "$GITHUB_OUTPUT"
          echo "tree=$(git rev-parse 'HEAD^{tree}')" >> "$GITHUB_OUTPUT"
`
				if full {
					workflow += "          git archive " + f.old + " old.txt | tar -xO | grep old.txt\n          test -z \"$(git remote)\"\n          test ! -e .git/objects/info/alternates\n"
				}
				in := Request{Repository: "file://" + f.remote, Revision: f.candidate, Workflow: workflow, JobID: "probe", EventName: "pull_request", Event: json.RawMessage(`{"number":42,"pull_request":{"base":{"ref":"main"},"head":{"ref":"topic"}}}`), Env: map[string]string{"KEEP": "value"}}
				assignments, err := Prepare(t.Context(), in, f.source)
				require.NoError(t, err)
				require.Len(t, assignments, 1, "planning must admit the repository-dependent PR job")
				require.Equal(t, []string{"colony-2"}, assignments[0].Labels)
				var log bytes.Buffer
				result, err := Execute(t.Context(), in, Options{Workdir: f.source, StateDir: t.TempDir(), Log: &log})
				require.NoError(t, err, log.String())
				require.Equal(t, "success", result.Conclusion, log.String())
				require.Equal(t, map[string]string{"repository": "colony-2/c2j", "owner": "colony-2", "head": f.candidate, "tree": f.tree}, result.Outputs, log.String())
				require.Equal(t, map[string]string{"KEEP": "value"}, in.Env, "the caller's environment is unchanged")
				require.Equal(t, f.candidate, historyGit(t, f.source, "rev-parse", "HEAD"))
			})
		}
	}
}

func TestRepositoryEnvironmentIsCredentialFree(t *testing.T) {
	for _, origin := range []string{"https://user:synthetic-origin-token@github.com/colony-2/c2j.git", "https://user:synthetic-origin-token@forge.example.test/team/repo.git?token=synthetic-query-token#synthetic-fragment-token"} {
		t.Run(origin[:strings.Index(origin, "@")], func(t *testing.T) {
			f := newHistoryFixture(t, "complete")
			historyGit(t, f.source, "remote", "set-url", "origin", origin)
			env := repositoryEnvironment(t.Context(), f.source, nil)
			require.NotEmpty(t, env["GITHUB_REPOSITORY"])
			for _, secret := range []string{"synthetic-origin-token", "synthetic-query-token", "synthetic-fragment-token"} {
				require.NotContains(t, env["GITHUB_REPOSITORY"], secret)
			}
			repo, err := prepareHistory(t.Context(), f.source, t.TempDir(), Request{Repository: "file://" + f.remote, Revision: f.candidate})
			require.NoError(t, err)
			require.Empty(t, historyGit(t, repo, "remote"))
			raw, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
			require.NoError(t, err)
			require.NotContains(t, string(raw), "synthetic-")
			require.NotContains(t, string(raw), f.source)
		})
	}
	values := map[string]string{"GITHUB_REPOSITORY": "override/project", "OTHER": "kept"}
	require.Equal(t, values, repositoryEnvironment(t.Context(), t.TempDir(), values), "explicit context retains precedence without requiring Git")
	require.Empty(t, repositoryEnvironment(t.Context(), t.TempDir(), nil), "no-option checkout still permits a non-Git source")
}
