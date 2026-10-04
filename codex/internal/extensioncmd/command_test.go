package extensioncmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2ops/codex/pkg/checkpoint"
	"github.com/stretchr/testify/require"
)

type testInput struct {
	Message string `json:"message"`
}

type testOutput struct {
	Reply string `json:"reply,omitempty"`
}

func TestDecodeInputDoesNotWaitForEOF(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		_, err := decodeInput[testInput](reader)
		done <- err
	}()
	_, err = writer.Write([]byte(`{"message":"hello"}`))
	require.NoError(t, err)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(100 * time.Millisecond):
		writer.Close()
		<-done
		t.Fatal("a complete input object blocked waiting for stdin EOF")
	}
}

func TestReadInputTimesOutWhenRunnerNeverSendsJSON(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	defer writer.Close()
	_, err = readInput[testInput](context.Background(), reader, 25*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "stdin forwarding")
}

func TestReadInputHonorsCancellation(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = readInput[testInput](ctx, reader, time.Minute)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRunWritesSuccessEnvelope(t *testing.T) {
	stdout, stderr, code := runWithCapturedStdio(t, []byte(`{"message":"hello"}`), func() int {
		return Run(func(ctx context.Context, input testInput) (Result[testOutput], error) {
			require.Equal(t, "hello", input.Message)
			return Result[testOutput]{
				Output:  testOutput{Reply: "world"},
				Objects: map[string]checkpoint.Draft{"session": {Type: "test.session/v1", Metadata: map[string]string{"id": "one"}, Files: map[string]string{"home": "/outbox/home"}}},
			}, nil
		})
	})

	require.Equal(t, 0, code)
	require.Contains(t, stderr, "input decoded")

	var env struct {
		Output  testOutput                  `json:"output"`
		Objects map[string]checkpoint.Draft `json:"objects"`
	}
	require.NoError(t, json.Unmarshal(stdout, &env))
	require.Equal(t, "world", env.Output.Reply)
	require.Equal(t, "test.session/v1", env.Objects["session"].Type)
}

func TestRunWritesErrorEnvelopeWhenResultPresent(t *testing.T) {
	stdout, stderr, code := runWithCapturedStdio(t, []byte(`{}`), func() int {
		return Run(func(ctx context.Context, input map[string]any) (Result[testOutput], error) {
			return Result[testOutput]{
				Output:  testOutput{Reply: "partial"},
				Objects: map[string]checkpoint.Draft{"session": {Type: "test.session/v1"}},
				ArtifactRefs: map[string]ArtifactRef{
					"logs": NewExternalArtifactRef("logs", "https://example.com/logs", true),
				},
			}, errors.New("boom")
		})
	})

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "boom")
	require.NotContains(t, string(stdout), `"objects"`)

	var env struct {
		Output       testOutput             `json:"output"`
		ArtifactRefs map[string]ArtifactRef `json:"artifact_refs"`
	}
	require.NoError(t, json.Unmarshal(stdout, &env))
	require.Equal(t, "partial", env.Output.Reply)
	require.Contains(t, env.ArtifactRefs, "logs")
	require.Equal(t, "https://example.com/logs", env.ArtifactRefs["logs"].External.URL)
}

func runWithCapturedStdio(t *testing.T, stdin []byte, fn func() int) ([]byte, string, int) {
	t.Helper()

	tmpDir := t.TempDir()
	stdinPath := filepath.Join(tmpDir, "stdin.json")
	require.NoError(t, os.WriteFile(stdinPath, stdin, 0o644))

	stdinFile, err := os.Open(stdinPath)
	require.NoError(t, err)
	defer stdinFile.Close()

	stdoutReader, stdoutWriter, err := os.Pipe()
	require.NoError(t, err)
	defer stdoutReader.Close()

	stderrReader, stderrWriter, err := os.Pipe()
	require.NoError(t, err)
	defer stderrReader.Close()

	oldStdin := os.Stdin
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	os.Stdin = stdinFile
	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	code := fn()

	require.NoError(t, stdoutWriter.Close())
	require.NoError(t, stderrWriter.Close())

	stdout, err := io.ReadAll(stdoutReader)
	require.NoError(t, err)
	stderr, err := io.ReadAll(stderrReader)
	require.NoError(t, err)

	return stdout, string(stderr), code
}
