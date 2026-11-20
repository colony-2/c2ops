package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Open and pin each directory; compare its identity after opening to close the
// Lstat/OpenRoot race. Root confines later accesses even if a directory moves.
func descend(root *os.Root, name string) (*os.Root, error) {
	info, e := root.Lstat(name)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("directory missing or symlinked")
	}
	next, e := root.OpenRoot(name)
	if e != nil {
		return nil, errors.New("cannot open directory")
	}
	actual, e := next.Stat(".")
	if e != nil || !os.SameFile(info, actual) {
		next.Close()
		return nil, errors.New("directory changed while opening")
	}
	return next, nil
}
func openRoot(name string) (*os.Root, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, errors.New("roots must be absolute canonical paths")
	}
	r, e := os.OpenRoot("/")
	if e != nil {
		return nil, e
	}
	for _, p := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
		if p == "" {
			continue
		}
		next, e := descend(r, p)
		r.Close()
		if e != nil {
			return nil, e
		}
		r = next
	}
	return r, nil
}
func regular(root *os.Root, name string) (*os.File, error) {
	if !safeName(name) {
		return nil, errors.New("unsafe artifact name")
	}
	parts := strings.Split(name, "/")
	r := root
	for _, p := range parts[:len(parts)-1] {
		next, e := descend(r, p)
		if r != root {
			r.Close()
		}
		if e != nil {
			return nil, e
		}
		r = next
	}
	if r != root {
		defer r.Close()
	}
	// NONBLOCK avoids hanging on FIFOs; NOFOLLOW also rejects an internal symlink
	// swapped in after metadata validation. Supported hosts are Linux and Darwin.
	f, e := r.OpenFile(parts[len(parts)-1], os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, errors.New("cannot open regular artifact")
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("artifact must be a regular file")
	}
	return f, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func hashReader(ctx context.Context, r io.Reader) (int64, string, error) {
	h := sha256.New()
	n, e := io.Copy(h, contextReader{ctx, r})
	return n, hex.EncodeToString(h.Sum(nil)), e
}
func fileDigest(ctx context.Context, root *os.Root, name string) (int64, string, error) {
	f, e := regular(root, name)
	if e != nil {
		return 0, "", e
	}
	n, h, e := hashReader(ctx, f)
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	return n, h, e
}
func snapshot(ctx context.Context, root *os.Root, d Object, stage *os.Root, dest string) error {
	f, e := regular(root, str(d, "name"))
	if e != nil {
		return e
	}
	out, e := stage.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		f.Close()
		return errors.New("cannot stage binary")
	}
	expected, _ := number(d, "size")
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, h), contextReader{ctx, io.LimitReader(f, expected+1)})
	ce := out.Close()
	ie := f.Close()
	if e != nil || ce != nil || ie != nil {
		return errors.New("cannot snapshot binary")
	}
	if n != expected || hex.EncodeToString(h.Sum(nil)) != str(d, "sha256") {
		return errors.New("binary size/sha256 mismatch")
	}
	return nil
}
func validateRoots(r Request) error {
	roots := []string{r.Worktree, r.Inbox, r.Outbox}
	for a := range roots {
		for b := 0; b < a; b++ {
			if roots[a] == "/" || roots[b] == "/" || roots[a] == roots[b] || strings.HasPrefix(roots[a], roots[b]+"/") || strings.HasPrefix(roots[b], roots[a]+"/") {
				return errors.New("workspace, inbox and outbox must not overlap")
			}
		}
	}
	return nil
}

// Publish by exclusive create, never rename over caller files. Existing outputs
// are reusable only after confined regular-file reads and digest verification.
func promote(ctx context.Context, root *os.Root, stage *os.Root, files []Object) error {
	for _, d := range files {
		if _, e := root.Lstat(str(d, "name")); e == nil {
			if e = matching(ctx, root, d); e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return errors.New("cannot inspect output destination")
		}
	}
	for _, d := range files {
		if e := ctx.Err(); e != nil {
			return e
		}
		name := str(d, "name")
		out, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if os.IsExist(e) {
			if e = matching(ctx, root, d); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			return errors.New("cannot create output")
		}
		in, e := regular(stage, name)
		if e != nil {
			out.Close()
			return errors.New("cannot read staged output")
		}
		_, e = io.Copy(out, contextReader{ctx, in})
		ie := in.Close()
		oe := out.Close()
		if e != nil || ie != nil || oe != nil {
			return errors.New("output copy failed; retry requires revalidation")
		}
		if e = matching(ctx, root, d); e != nil {
			return e
		}
	}
	return nil
}
func matching(ctx context.Context, root *os.Root, d Object) error {
	f, e := regular(root, str(d, "name"))
	if e != nil {
		return e
	}
	n, h, e := hashReader(ctx, f)
	ce := f.Close()
	want, _ := number(d, "size")
	if e != nil || ce != nil || n != want || h != str(d, "sha256") {
		return errors.New("conflicting output bytes")
	}
	return nil
}

// The whole staging lifetime stays relative to the already-open output root.
// A caller moving/rebinding its pathname cannot redirect temporary writes.
func newStage(outbox *os.Root) (*os.Root, string, error) {
	name := ".release-pack-" + rand.Text()
	if e := outbox.Mkdir(name, 0700); e != nil {
		return nil, "", errors.New("cannot create private staging")
	}
	stage, e := descend(outbox, name)
	if e != nil {
		outbox.RemoveAll(name)
		return nil, "", e
	}
	return stage, name, nil
}
