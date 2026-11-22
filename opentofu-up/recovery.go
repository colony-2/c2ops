package main

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"golang.org/x/sys/unix"
)

type objectDraft struct {
	Type     string            `json:"type"`
	Metadata any               `json:"metadata"`
	Files    map[string]string `json:"files"`
}

func openOutbox(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errInvalid
	}
	info, e := os.Lstat(path)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errInvalid
	}
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil {
		return nil, errInvalid
	}
	fd, e := unix.Open(resolved, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, errInvalid
	}
	return os.NewFile(uintptr(fd), path), nil
}
func ciphertextFile(out *os.File) (*os.File, error) {
	random := make([]byte, 16)
	if _, e := rand.Read(random); e != nil {
		return nil, errInvalid
	}
	name := "recovery-" + hex.EncodeToString(random) + ".age"

	fd, e := unix.Openat(int(out.Fd()), name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return nil, errInvalid
	}
	return os.NewFile(uintptr(fd), filepath.Join(out.Name(), name)), nil
}
func preflightRecovery(path string, recipients []age.Recipient) (*os.File, error) {
	out, e := openOutbox(path)
	if e != nil {
		return nil, errInvalid
	}
	f, e := ciphertextFile(out)
	if e != nil {
		out.Close()
		return nil, errInvalid
	}
	keepOut := false
	defer func() {
		f.Close()
		_ = unix.Unlinkat(int(out.Fd()), filepath.Base(f.Name()), 0)
		if !keepOut {
			out.Close()
		}
	}()
	w, e := age.Encrypt(f, recipients...)
	if e == nil {
		_, e = w.Write([]byte("recovery preflight"))
		if e == nil {
			e = w.Close()
		}
	}
	if e == nil {
		e = f.Sync()
	}
	if e != nil {
		return nil, errInvalid
	}
	keepOut = true
	return out, nil
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
func emergency(ctx context.Context, s snapshot, out *os.File, recipients []age.Recipient, i Input, stage string) (*objectDraft, error) {
	// Open only known locations. O_NOFOLLOW rejects hostile emergency symlinks.
	names := []string{"errored.tfstate", "terraform.tfstate", "terraform.tfstate.backup"}
	type state struct {
		name string
		f    *os.File
		size int64
	}
	states := []state{}
	defer func() {
		for _, s := range states {
			s.f.Close()
		}
	}()
	rootFD, e := openOutbox(s.Root)
	if e != nil {
		return nil, errInvalid
	}
	defer rootFD.Close()
	var total int64
	for _, name := range names {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p := filepath.Join(s.Root, name)
		fd, e := unix.Openat(int(rootFD.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e == unix.ENOENT {
			continue
		}
		if e != nil {
			return nil, errInvalid
		}
		f := os.NewFile(uintptr(fd), p)
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() {
			f.Close()
			return nil, errInvalid
		}
		total += st.Size()
		if total > 128<<20 {
			f.Close()
			return nil, errInvalid
		}
		states = append(states, state{name, f, st.Size()})
	}
	if len(states) == 0 {
		return nil, nil
	}
	f, e := ciphertextFile(out)
	if e != nil {
		return nil, errInvalid
	}
	complete := false
	defer func() {
		f.Close()
		if !complete {
			_ = unix.Unlinkat(int(out.Fd()), filepath.Base(f.Name()), 0)
		}
	}()
	w, e := age.Encrypt(f, recipients...)
	if e != nil {
		return nil, errInvalid
	}
	tw := tar.NewWriter(w)
	labels := []string{}
	for _, st := range states {
		labels = append(labels, st.name)
	}
	index := map[string]any{"format": "age-tar/v1", "files": labels}
	indexBytes, _ := json.Marshal(index)
	if e = tw.WriteHeader(&tar.Header{Name: "index.json", Mode: 0600, Size: int64(len(indexBytes)), Typeflag: tar.TypeReg}); e != nil {
		return nil, errInvalid
	}
	if _, e = tw.Write(indexBytes); e != nil {
		return nil, errInvalid
	}
	for _, st := range states {
		if strings.Contains(st.name, "/") {
			return nil, errInvalid
		}
		if e = tw.WriteHeader(&tar.Header{Name: "state/" + st.name, Mode: 0600, Size: st.size, Typeflag: tar.TypeReg}); e != nil {
			return nil, errInvalid
		}
		if _, e = io.CopyN(tw, contextReader{ctx, st.f}, st.size); e != nil {
			return nil, errInvalid
		}
	}
	if tw.Close() != nil || w.Close() != nil || f.Sync() != nil || ctx.Err() != nil {
		return nil, errInvalid
	}
	if f.Close() != nil {
		return nil, errInvalid
	}
	complete = true
	return &objectDraft{Type: "c2ops.opentofu.recovery/v1", Metadata: map[string]any{"format": "age-tar/v1", "revision": i.Revision, "root": i.Root, "backend": i.Backend, "stage": stage}, Files: map[string]string{"ciphertext": f.Name()}}, nil
}
