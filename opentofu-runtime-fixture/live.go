package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
)

// Only fixed harness categories may cross the private live runtime boundary.
type liveError string

func (e liveError) Error() string { return string(e) }

func (p *provider) forwardSecret(ctx context.Context, r jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	fail := func() (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{}, fmt.Errorf("parent named secret unavailable")
	}
	if p.deny.Load() || len(r.Names) != 1 {
		return fail()
	}
	name := r.Names[0]
	allowed := false
	for _, n := range p.names {
		if n == name {
			allowed = true
		}
	}
	if !allowed || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,255}$`).MatchString(name) {
		return fail()
	}
	cmd := exec.CommandContext(ctx, p.parentHelper, "secret", "get", name, "--format=raw")
	cmd.Env = p.parentEnv
	b, err := cmd.Output()
	if err != nil || len(b) == 0 || len(b) > 1<<20 {
		return fail()
	}
	return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{name: {Value: string(b)}}}, nil
}
func liveResult(ctx context.Context, rt *sqlite.Runtime, k jobdb.JobKey, data workflow.TaskData, p *provider, c config) error {
	// Copy only hydrated ciphertext; never export the runtime database or raw logs.
	chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: k})
	if err != nil {
		return liveError("native result unavailable")
	}
	var retained []byte
	for _, chapter := range chapters {
		for _, stored := range chapter.Artifacts {
			if stored.Name != "recovery.age" {
				continue
			}
			a, e := rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: k, Ordinal: chapter.Ordinal, Name: stored.Name, Digest: stored.Digest})
			if e != nil {
				return liveError("native ciphertext unavailable")
			}
			r, e := a.Open()
			if e != nil {
				return liveError("native ciphertext unavailable")
			}
			b, e := io.ReadAll(io.LimitReader(r, 256<<20+1))
			_ = r.Close()
			if e != nil || len(b) > 256<<20 || !bytes.HasPrefix(b, []byte("age-encryption.org/v1")) {
				return liveError("invalid native ciphertext")
			}
			if retained != nil {
				if !bytes.Equal(retained, b) {
					return liveError("ambiguous native ciphertext")
				}
				continue // The runtime may export the same artifact again at recipe completion.
			}
			f, e := os.OpenFile(c.RecoveryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				if os.IsExist(e) {
					return liveError("ciphertext target already exists")
				}
				return liveError("ciphertext retention failed")
			}
			_, e = f.Write(b)
			ce := f.Close()
			if e != nil || ce != nil {
				_ = os.Remove(c.RecoveryPath)
				return liveError("ciphertext retention failed")
			}
			retained = b
		}
	}
	if p.calls.Load() != 1 {
		return liveError("incorrect operation resolution count")
	}
	v, err := data.GetData()
	if err != nil {
		return liveError("native result unavailable")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return liveError("native result unavailable")
	}
	f, err := os.OpenFile(c.ResultPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return liveError("native result retention failed")
	}
	_, err = f.Write(b)
	ce := f.Close()
	if err != nil || ce != nil {
		return liveError("native result retention failed")
	}
	fmt.Println("native fresh invocation; one parent named-secret resolution")
	return nil
}
