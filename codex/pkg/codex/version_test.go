package codex

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexVersionMinimum(t *testing.T) {
	for _, version := range []string{"0.148.0", "0.148.1", "0.157.1", "0.162.1", "0.200.0", "1.0.0", "0.148.0+build.1", "0.149.0-alpha.1"} {
		t.Run(version, func(t *testing.T) { require.NoError(t, validateCodexVersion(version)) })
	}
	for _, version := range []string{"0.99.0", "0.147.99", "0.148.0-alpha.1", "", "future", "0.148", "v0.148.0", "0.0148.0", "0.149.0-alpha.01", "0.148.0 garbage", "999999999999999999999999.0.0"} {
		t.Run("reject_"+version, func(t *testing.T) { require.ErrorContains(t, validateCodexVersion(version), "0.148.0 or later") })
	}
}

func TestPrepareSessionVersionAndProducerMetadata(t *testing.T) {
	for _, version := range []string{"0.147.0", "0.148.0", "0.162.1", "1.0.0"} {
		t.Run(version, func(t *testing.T) {
			installFakeCodex(t, fmt.Sprintf("#!/bin/sh\nprintf 'codex-cli %s\\n'\n", version))
			t.Setenv("C2J_OBJECT_OUTBOX", t.TempDir())
			paths := execRunPaths{Workdir: t.TempDir(), Worktree: t.TempDir(), Inbox: t.TempDir()}
			state, err := prepareSession(context.Background(), nil, paths, nil)
			if version == "0.147.0" {
				require.ErrorContains(t, err, "0.148.0 or later")
				return
			}
			require.NoError(t, err)
			defer state.close()
			seedTestSession(t, state.home, "id")
			state.id = "id"
			_, drafts, err := state.publish()
			require.NoError(t, err)
			metadata := drafts["session"].Metadata.(SessionMetadata)
			require.Equal(t, version, metadata.RuntimeVersion)

			// Restore an existing v1 checkpoint from a different supported CLI.
			input := &SessionInput{Metadata: metadata, Files: drafts["session"].Files}
			input.Metadata.RuntimeVersion = "0.157.1"
			resumed, err := prepareSession(context.Background(), input, paths, nil)
			require.NoError(t, err)
			resumed.close()
			input.Metadata.RuntimeVersion = "0.147.0"
			_, err = prepareSession(context.Background(), input, paths, nil)
			require.ErrorContains(t, err, "unsupported checkpoint producer")
		})
	}
}
