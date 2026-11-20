package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"time"
)

func descriptor(i Object, name, kind string, n int64, h string, t *Target) Object {
	d := Object{}
	for _, k := range identity {
		d[k] = i[k]
	}
	d["name"] = name
	d["kind"] = kind
	d["size"] = json.Number(fmt.Sprint(n))
	d["sha256"] = h
	if t != nil {
		d["goos"] = t.GOOS
		d["goarch"] = t.GOARCH
	}
	return d
}
func writeArchive(ctx context.Context, w io.Writer, binary string, stage *os.Root, snapshot string, docs []document) error {
	gz, e := gzip.NewWriterLevel(w, gzip.BestCompression)
	if e != nil {
		return e
	}
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	add := func(name string, mode, size int64, dir bool, r io.Reader) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		kind := byte(tar.TypeReg)
		if dir {
			kind = tar.TypeDir
		}
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, Typeflag: kind, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX}); e != nil {
			return e
		}
		if r != nil {
			_, e := io.Copy(tw, contextReader{ctx, r})
			return e
		}
		return nil
	}
	f, e := regular(stage, snapshot)
	if e != nil {
		return e
	}
	info, e := f.Stat()
	if e == nil {
		e = add(binary, 0755, info.Size(), false, f)
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	// Build a sorted union of documents and parent directories.
	entries := map[string]*document{}
	for idx := range docs {
		d := &docs[idx]
		entries[d.Name] = d
		for p := path.Dir(d.Name); p != "."; p = path.Dir(p) {
			entries[p+"/"] = nil
		}
	}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if e != nil {
			break
		}
		d := entries[n]
		if d == nil {
			e = add(n, 0755, 0, true, nil)
		} else {
			e = add(n, d.Mode, int64(len(d.Data)), false, bytes.NewReader(d.Data))
		}
	}
	te := tw.Close()
	ge := gz.Close()
	if e != nil {
		return e
	}
	if te != nil {
		return te
	}
	return ge
}

func pack(ctx context.Context, r Request) ([]byte, error) {
	if e := r.validate(); e != nil {
		return nil, e
	}
	if e := validateRoots(r); e != nil {
		return nil, e
	}
	// Open all bound roots before reading source or payloads.
	source, e := openRoot(r.Worktree)
	if e != nil {
		return nil, e
	}
	defer source.Close()
	inbox, e := openRoot(r.Inbox)
	if e != nil {
		return nil, e
	}
	defer inbox.Close()
	outbox, e := openRoot(r.Outbox)
	if e != nil {
		return nil, e
	}
	defer outbox.Close()
	docs, tree, e := readDocuments(ctx, r)
	if e != nil {
		return nil, e
	}
	stage, stageName, e := newStage(outbox)
	if e != nil {
		return nil, errors.New("cannot create private staging")
	}
	defer outbox.RemoveAll(stageName)
	defer stage.Close()
	selected := append([]Object(nil), r.Files...)
	sort.Slice(selected, func(a, b int) bool {
		return archiveName(r.Intent, Target{str(selected[a], "goos"), str(selected[a], "goarch")}) < archiveName(r.Intent, Target{str(selected[b], "goos"), str(selected[b], "goarch")})
	})
	for idx, d := range selected {
		if e = snapshot(ctx, inbox, d, stage, fmt.Sprint(idx)); e != nil {
			return nil, e
		}
	}
	files := []Object{}
	checksums := ""
	for idx, d := range selected {
		t := Target{str(d, "goos"), str(d, "goarch")}
		name := archiveName(r.Intent, t)
		f, e := stage.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, errors.New("cannot create staged archive")
		}
		e = writeArchive(ctx, f, str(r.Intent, "binary_name"), stage, fmt.Sprint(idx), docs)
		ce := f.Close()
		if e != nil || ce != nil {
			return nil, errors.New("archive write/close failed")
		}
		n, h, e := fileDigest(ctx, stage, name)
		if e != nil {
			return nil, errors.New("cannot hash final archive")
		}
		files = append(files, descriptor(r.Intent, name, "archive", n, h, &t))
		checksums += h + "  " + name + "\n"
	}
	if e = stage.WriteFile("checksums.txt", []byte(checksums), 0600); e != nil {
		return nil, errors.New("cannot write checksums")
	}
	n, h, e := fileDigest(ctx, stage, "checksums.txt")
	if e != nil {
		return nil, e
	}
	files = append(files, descriptor(r.Intent, "checksums.txt", "checksums", n, h, nil))
	docRecords := []Object{}
	for _, d := range docs {
		sum := sha256.Sum256(d.Data)
		docRecords = append(docRecords, Object{"name": d.Name, "git_blob": d.Blob, "git_mode": fmt.Sprintf("100%03o", d.Mode), "size": len(d.Data), "sha256": hex.EncodeToString(sum[:])})
	}
	result := Object{"output": Object{"intent": r.Intent, "files": files, "provenance": Object{"schema_version": 1, "operation": "release.pack", "archive_policy": "release-pack-v1", "source_commit": str(r.Intent, "source_commit"), "source_tree": tree, "selected_inputs": selected, "documents": docRecords}}}
	b, e := json.Marshal(result)
	if e != nil {
		return nil, errors.New("cannot serialize result")
	}
	if len(b)+1 > jsonLimit {
		return nil, errors.New("result exceeds 128 KiB")
	}
	if e = promote(ctx, outbox, stage, files); e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return append(b, '\n'), nil
}
