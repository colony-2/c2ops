package codex

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/require"
)

type artifactValidatingJobWorker struct {
	inner swf.JobWorker
	t     *testing.T
}

func (w *artifactValidatingJobWorker) Name() string {
	return w.inner.Name()
}

func (w *artifactValidatingJobWorker) Run(ctx swf.JobContext, data swf.JobData) (swf.JobData, error) {
	validator := &artifactValidatingJobContext{inner: ctx, t: w.t}
	return w.inner.Run(validator, data)
}

type artifactValidatingJobContext struct {
	inner swf.JobContext
	t     *testing.T
}

func (c *artifactValidatingJobContext) AwaitJobs(jobIds ...string) error {
	return nil
}

func (c *artifactValidatingJobContext) GetJobKey() swf.JobKey {
	return c.inner.GetJobKey()
}

func (c *artifactValidatingJobContext) Logger() *slog.Logger {
	return c.inner.Logger()
}

func (c *artifactValidatingJobContext) DoTask(policy swf.RunPolicy, taskType string, data swf.TaskData) (swf.TaskData, error) {
	out, err := c.inner.DoTask(policy, taskType, data)
	if err != nil {
		return out, err
	}
	if strings.HasPrefix(taskType, "codex.exec") {
		validateCodexArtifacts(c.t, context.Background(), out)
	}
	return out, nil
}

func (c *artifactValidatingJobContext) AwaitDuration(waitFor swf.Duration) error {
	return c.inner.AwaitDuration(waitFor)
}

func validateCodexArtifacts(t *testing.T, ctx context.Context, out swf.TaskData) {
	t.Helper()
	arts, err := out.GetArtifacts()
	require.NoError(t, err)

	var stdout []byte
	var stderr []byte
	for _, art := range arts {
		switch art.Name() {
		case "stdout.jsonl":
			data, err := art.Bytes(ctx)
			require.NoError(t, err)
			stdout = data
		case "stderr.txt":
			data, err := art.Bytes(ctx)
			require.NoError(t, err)
			stderr = data
		}
	}

	require.NotEmpty(t, stdout, "missing stdout.jsonl artifact")
	validateJSONL(t, stdout)
	require.Empty(t, filterIgnorableCodexStderr(stderr), "stderr.txt should be empty")
}

func validateJSONL(t *testing.T, content []byte) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	require.NotEmpty(t, lines, "stdout jsonl is empty")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("invalid jsonl at line %d: %v\nline=%q", i+1, err, line)
		}
	}
}

func filterIgnorableCodexStderr(stderr []byte) []byte {
	if len(stderr) == 0 {
		return nil
	}
	lines := strings.Split(string(stderr), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "WARNING: proceeding, even though we could not update PATH: Refusing to create helper binaries under temporary dir") {
			continue
		}
		if trimmed == "Reading additional input from stdin..." {
			continue
		}
		filtered = append(filtered, line)
	}
	if len(filtered) == 0 {
		return nil
	}
	return []byte(strings.Join(filtered, "\n"))
}
