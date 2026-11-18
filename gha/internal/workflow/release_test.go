package workflow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testRevision = "0123456789abcdef0123456789abcdef01234567"
const testRepository = "github:colony-2/c2ops"
const testPrefix = "nix:" + testRepository + "/" + testRevision

func TestResolveWorkers(t *testing.T) {
	for _, helper := range []string{"", "nix:github:colony-2/c2ops/published#gha-workflow", " ", "bad helper"} {
		for _, executor := range []string{"", "nix:github:colony-2/c2ops/other#gha-job", "\t", "bad executor"} {
			in := Input{HelperOp: helper, ExecutorOp: executor, Workflow: "unchanged"}
			got, err := resolveWorkers(in, testRepository, testRevision)
			require.NoError(t, err)
			expected := in
			if helper == "" {
				expected.HelperOp = testPrefix + "#gha-workflow"
			}
			if executor == "" {
				expected.ExecutorOp = testPrefix + "#gha-job"
			}
			require.Equal(t, expected, got)
		}
	}
}

func TestMissingReleaseMetadata(t *testing.T) {
	for _, metadata := range [][2]string{{"", ""}, {testRepository, ""}, {testRepository, "abc"}, {testRepository, strings.Repeat("A", 40)}, {testRepository, strings.Repeat("0", 39)}, {testRepository, strings.Repeat("0", 41)}, {"github:fork/c2ops", testRevision}, {testRepository + "/", testRevision}} {
		for _, in := range []Input{{}, {HelperOp: "explicit"}, {ExecutorOp: "explicit"}, {HelperOp: "explicit", ExecutorOp: "explicit"}} {
			got, err := resolveWorkers(in, metadata[0], metadata[1])
			if in.HelperOp != "" && in.ExecutorOp != "" {
				require.NoError(t, err)
				require.Equal(t, in, got)
				continue
			}
			require.Error(t, err)
			require.Equal(t, in.HelperOp == "", strings.Contains(err.Error(), "helper_op"))
			require.Equal(t, in.ExecutorOp == "", strings.Contains(err.Error(), "executor_op"))
		}
	}
}
