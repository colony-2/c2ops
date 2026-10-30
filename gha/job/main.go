// gha-job is the single-job execution helper for external orchestration.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2ops/gha/internal/extensioncmd"
	"github.com/colony-2/c2ops/gha/internal/jobexec"
)

type input struct {
	jobexec.Request
	WorktreePath string `json:"worktree_path"`
}

func main() {
	extensioncmd.Main(run)
}

func run(ctx context.Context, in input) (extensioncmd.Result[jobexec.Result], error) {
	var out extensioncmd.Result[jobexec.Result]
	if !filepath.IsAbs(in.WorktreePath) {
		return out, fmt.Errorf("worktree_path must be absolute")
	}
	if filepath.IsAbs(in.Workflow) || in.Workflow == "" {
		return out, fmt.Errorf("workflow must be a worktree-relative file")
	}
	root, err := filepath.EvalSymlinks(in.WorktreePath)
	if err != nil {
		return out, err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, in.Workflow))
	if err != nil {
		return out, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return out, fmt.Errorf("workflow escapes worktree")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	outbox := os.Getenv("C2J_OBJECT_OUTBOX")
	if !filepath.IsAbs(outbox) {
		return out, fmt.Errorf("C2J_OBJECT_OUTBOX is required for durable job diagnostics")
	}
	artifacts, err := os.MkdirTemp(outbox, "gha-job-*")
	if err != nil {
		return out, err
	}
	logPath := filepath.Join(artifacts, "job.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		os.RemoveAll(artifacts)
		return out, err
	}
	in.Request.Workflow = string(content)
	result, runErr := jobexec.Execute(ctx, in.Request, jobexec.Options{
		Workdir: root, StateDir: artifacts, Log: io.MultiWriter(os.Stderr, log),
	})
	closeErr := log.Close()
	// c2j seals this outbox file into a JobDB artifact before cleaning the
	// invocation directory. An external file:// ref would not survive cleanup.
	result.Diagnostics = map[string]string{"$object": "diagnostics"}
	out.Output = result
	out.Objects = map[string]extensioncmd.ObjectDraft{
		"diagnostics": {Type: "c2ops.gha.diagnostics/v1", Metadata: map[string]string{"job_id": in.JobID}, Files: map[string]string{"log": logPath}},
	}
	if runErr != nil {
		return out, runErr
	}
	return out, closeErr
}
