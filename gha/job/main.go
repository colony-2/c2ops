// gha-job is the single-job execution helper for external orchestration.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2ops/gha/internal/extensioncmd"
	"github.com/colony-2/c2ops/gha/internal/jobexec"
)

type snapshot struct {
	Ref   any               `json:"ref"`
	Files map[string]string `json:"files"`
}
type input struct {
	ArtifactInputs []snapshot `json:"artifact_inputs,omitempty"`
	jobexec.Request
	Payload        string `json:"payload,omitempty"`
	WorkflowSHA256 string `json:"workflow_sha256,omitempty"`
	WorktreePath   string `json:"worktree_path"`
}

func main() {
	extensioncmd.Main(run)
}

func run(ctx context.Context, in input) (extensioncmd.Result[jobexec.Result], error) {
	var out extensioncmd.Result[jobexec.Result]
	if in.Payload != "" {
		raw, err := base64.StdEncoding.DecodeString(in.Payload)
		if err != nil {
			return out, err
		}
		secrets := in.Secrets
		if err := json.Unmarshal(raw, &in.Request); err != nil {
			return out, err
		}
		in.Secrets = secrets
	}
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
	if in.WorkflowSHA256 != "" {
		hash := sha256.Sum256(content)
		if hex.EncodeToString(hash[:]) != in.WorkflowSHA256 {
			return out, fmt.Errorf("workflow changed from the planned snapshot")
		}
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
	artifactDir := filepath.Join(artifacts, "uploaded")
	var sources []string
	for _, snapshot := range in.ArtifactInputs {
		path := snapshot.Files["data"]
		if !filepath.IsAbs(path) {
			return out, fmt.Errorf("artifact input requires hydrated data directory")
		}
		sources = append(sources, path)
	}
	result, runErr := jobexec.Execute(ctx, in.Request, jobexec.Options{
		ArtifactDir: artifactDir, ArtifactInputs: sources, Workdir: root, StateDir: artifacts, Log: io.MultiWriter(os.Stderr, log),
	})
	closeErr := log.Close()
	// c2j seals this outbox file into a JobDB artifact before cleaning the
	// invocation directory. An external file:// ref would not survive cleanup.
	result.Diagnostics = map[string]string{"$object": "diagnostics"}
	result.Artifacts = []any{map[string]string{"$object": "artifacts"}}
	out.Output = result
	out.Objects = map[string]extensioncmd.ObjectDraft{
		"artifacts":   {Type: "c2ops.gha.artifacts/v1", Metadata: map[string]string{"job_id": in.JobID}, Files: map[string]string{"data": artifactDir}},
		"diagnostics": {Type: "c2ops.gha.diagnostics/v1", Metadata: map[string]string{"job_id": in.JobID}, Files: map[string]string{"log": logPath}},
	}
	if runErr != nil {
		return out, runErr
	}
	return out, closeErr
}
