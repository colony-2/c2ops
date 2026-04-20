package codex

import (
	"context"
	"io"

	"github.com/colony-2/shai/pkg/shai"
)

type runnerAdapter struct {
	inner shai.Sandbox
}

func (r runnerAdapter) Run(ctx context.Context) error {
	return r.inner.Run(ctx)
}

func (r runnerAdapter) Close() error { return r.inner.Close() }

func defaultRunnerFactory(cfg *shai.SandboxConfig) (Runner, error) {
	runner, err := shai.NewSandbox(*cfg)
	if err != nil {
		return nil, err
	}
	return runnerAdapter{inner: runner}, nil
}

type outputCollector struct {
	stdout io.Writer
	stderr io.Writer
}

func newOutputCollector(stdout io.Writer, stderr io.Writer) *outputCollector {
	return &outputCollector{stdout: stdout, stderr: stderr}
}

func (c *outputCollector) OnStdout(data []byte) {
	if len(data) == 0 {
		return
	}
	if _, err := c.stdout.Write(data); err != nil {
		// best-effort: if we can't write stdout, we can't do much about it
		// the error will be caught when we try to close the file
	}
}

func (c *outputCollector) OnStderr(data []byte) {
	if len(data) == 0 {
		return
	}
	if _, err := c.stderr.Write(data); err != nil {
		// best-effort: if we can't write stderr, we can't do much about it
		// the error will be caught when we try to close the file
	}
}

// stdoutWriter adapts OnStdout to an io.Writer for shai's non-TTY mode.
func (c *outputCollector) stdoutWriter() io.Writer {
	return writerAdapter(func(p []byte) { c.OnStdout(p) })
}

// stderrWriter adapts OnStderr to an io.Writer for shai's non-TTY mode.
func (c *outputCollector) stderrWriter() io.Writer {
	return writerAdapter(func(p []byte) { c.OnStderr(p) })
}

type writerAdapter func([]byte)

func (w writerAdapter) Write(p []byte) (int, error) {
	if w == nil {
		return len(p), nil
	}
	w(p)
	return len(p), nil
}
