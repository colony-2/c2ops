package jobexec

import (
	"context"
	"net/url"

	actgit "github.com/nektos/act/pkg/common/git"
)

// repositoryEnvironment freezes act's existing repository lookup before history
// preparation removes origin. Keep only metadata, never a credential-bearing
// remote, and use the same lookup for planning and execution.
func repositoryEnvironment(ctx context.Context, workdir string, values map[string]string) map[string]string {
	env := copyMap(values)
	if env["GITHUB_REPOSITORY"] != "" {
		return env
	}
	repository, err := actgit.FindGithubRepo(ctx, workdir, "github.com", "")
	if err != nil {
		return env // Preserve act's fallback for checkouts without an origin.
	}
	if sshSource.MatchString(repository) {
		env["GITHUB_REPOSITORY"] = repository
		return env
	}
	// act returns the locator itself for unrecognized hosts. Strip URL auth,
	// query and fragment rather than forwarding them as repository metadata.
	u, err := url.Parse(repository)
	if err != nil {
		return env
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	env["GITHUB_REPOSITORY"] = u.String()
	return env
}
