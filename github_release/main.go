package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func execute(ctx context.Context, r io.Reader, w io.Writer, token string, g *github) error {
	in, err := decodeInput(r)
	if err != nil {
		return err
	}
	if err = validate(in); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("explicit GITHUB_TOKEN authorization is required")
	}
	tmp, err := stage(in)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// Retain exact validated files before effects. This also proves outbox writeability.
	if err = retain(in, tmp); err != nil {
		return err
	}
	if g == nil {
		g = newGitHub(token)
	}
	output, err := g.run(ctx, in, tmp)
	if err != nil {
		// Only our fixed stage names and typed numeric IDs enter retained evidence.
		// Never retain remote bodies, request URLs, credentials or transport errors.
		if root, e := os.OpenRoot(in.OutboxRoot); e == nil {
			if root.MkdirAll("error", 0700) == nil {
				if f, e := root.OpenFile(filepath.Join("error", "github-release.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); e == nil {
					_ = json.NewEncoder(f).Encode(g.evidence)
					f.Close()
				}
			}
			root.Close()
		}
		return fmt.Errorf("%s: %w; tags, drafts and assets are retained for retry", g.evidence.Stage, err)
	}
	envelope := struct {
		Output Output `json:"output"`
	}{output}
	b, err := json.Marshal(envelope)
	if err != nil || len(b) > jsonLimit {
		return errors.New("result exceeds 128 KiB")
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Stdin, os.Stdout, os.Getenv("GITHUB_TOKEN"), nil); err != nil {
		fmt.Fprintln(os.Stderr, "github.release:", err)
		os.Exit(1)
	}
}
