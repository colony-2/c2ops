package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type progressKey struct{}
type progressLog struct {
	mu   sync.Mutex
	file *os.File
}

// Record operational phases, never prompts, environment values or CLI arguments.
// c2j buffers process stderr; the outbox file is also readable while the op runs.
func withProgress(ctx context.Context, outbox string) (context.Context, func(), error) {
	if err := os.MkdirAll(outbox, 0700); err != nil {
		return ctx, nil, err
	}
	file, err := os.OpenFile(filepath.Join(outbox, "codex-progress.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return ctx, nil, err
	}
	log := &progressLog{file: file}
	ctx = context.WithValue(ctx, progressKey{}, log)
	progress(ctx, "invocation.start", map[string]any{"pid": os.Getpid()})
	return ctx, func() { _ = file.Close() }, nil
}

func progress(ctx context.Context, phase string, fields map[string]any) {
	if ctx == nil {
		return
	}
	log, _ := ctx.Value(progressKey{}).(*progressLog)
	if log == nil {
		return
	}
	entry := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "phase": phase}
	for key, value := range fields {
		entry[key] = value
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	_, _ = fmt.Fprintln(log.file, string(data))
	_, _ = fmt.Fprintln(os.Stderr, "codex-op: "+string(data))
}
