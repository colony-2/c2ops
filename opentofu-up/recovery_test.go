package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
)

// Fail a selected encryption attempt while retaining the real age writer for
// every earlier attempt. No production encryption override is needed.
type failingRecipient struct {
	age.Recipient
	calls, failAt int
}

func (r *failingRecipient) Wrap(key []byte) ([]*age.Stanza, error) {
	r.calls++
	if r.calls == r.failAt {
		return nil, errors.New("private-encryption-diagnostic-sentinel")
	}
	return r.Recipient.Wrap(key)
}

func TestRecoveryFailures(t *testing.T) {
	for _, kind := range []string{"preflight-encryption", "recovery-encryption", "recovery-write"} {
		t.Run(kind, func(t *testing.T) {
			i, key := fixture(t)
			calls := []command{}
			reads := 0
			run := fakeRunner(t, &calls, 2, "apply")
			d := deps(t, run, time.Now(), &reads)
			wantStage := "recovery"
			switch kind {
			case "preflight-encryption":
				i.recipients = []age.Recipient{&failingRecipient{Recipient: key.Recipient(), failAt: 1}}
				wantStage = "preflight"
			case "recovery-encryption":
				i.recipients = []age.Recipient{&failingRecipient{Recipient: key.Recipient(), failAt: 2}}
			case "recovery-write":
				d.run = func(ctx context.Context, c command) runResult {
					if c.Args[0] == "apply" {
						// Remove the real, successfully preflighted outbox before
						// recovery creates its file through the retained directory FD.
						if e := os.Remove(d.outbox); e != nil {
							t.Fatal(e)
						}
					}
					return run(ctx, c)
				}
			}
			env, e := execute(context.Background(), i, d)
			if e != nil || env.Output.OK || env.Output.Outcome != "failed" || env.Output.Stage != wantStage || env.Output.Category == nil || *env.Output.Category != wantStage {
				t.Fatalf("unexpected failure result: %+v %v", env.Output, e)
			}
			if reads != 1 || env.Output.Recovery != nil || len(env.Objects) != 0 {
				t.Fatal("failed recovery returned an object or changed secret resolution")
			}
			if wantStage == "preflight" {
				if len(calls) != 0 {
					t.Fatal("encryption preflight failure ran OpenTofu")
				}
			} else {
				if len(calls) != 5 || calls[4].Args[0] != "apply" {
					t.Fatal("failed apply retried or did not reach recovery")
				}
				if _, e := os.Stat(filepath.Dir(calls[4].Dir)); !os.IsNotExist(e) {
					t.Fatal("private invocation survived recovery failure")
				}
			}
			entries, e := os.ReadDir(d.outbox)
			if (e != nil && !os.IsNotExist(e)) || len(entries) != 0 {
				t.Fatal("partial ciphertext survived failed recovery", e)
			}
			b, e := json.Marshal(env)
			if e != nil {
				t.Fatal(e)
			}
			for _, sentinel := range []string{"private-token", "private-state", "private-plan", "private-encryption-diagnostic"} {
				if bytes.Contains(b, []byte(sentinel)) {
					t.Fatal("private data in failed recovery result")
				}
			}
		})
	}
}
