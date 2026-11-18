package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

// Set only for the two submitting packages by the release build. Raw Go and
// unversioned Nix builds require explicit workers instead of following published.
var releaseRepository string
var releaseRevision string

var fullRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

func resolveWorkers(in Input, repository, revision string) (Input, error) {
	missing := []string{}
	if in.HelperOp == "" {
		missing = append(missing, "helper_op")
	}
	if in.ExecutorOp == "" {
		missing = append(missing, "executor_op")
	}
	if len(missing) == 0 {
		return in, nil
	}
	if repository != "github:colony-2/c2ops" || !fullRevision.MatchString(revision) {
		return in, fmt.Errorf("matching GHA release metadata unavailable; set %s explicitly", strings.Join(missing, " and "))
	}
	prefix := "nix:" + repository + "/" + revision
	if in.HelperOp == "" {
		in.HelperOp = prefix + "#gha-workflow"
	}
	if in.ExecutorOp == "" {
		in.ExecutorOp = prefix + "#gha-job"
	}
	return in, nil
}
