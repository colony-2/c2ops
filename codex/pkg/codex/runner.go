package codex

import (
	"io"
)

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

// stdoutWriter adapts OnStdout to an io.Writer.
func (c *outputCollector) stdoutWriter() io.Writer {
	return writerAdapter(func(p []byte) { c.OnStdout(p) })
}

// stderrWriter adapts OnStderr to an io.Writer.
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
