package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// A limit is an incomplete scan error, never a clean result. Inspect underlying
// byte fields before JSON marshaling can conceal them with base64 encoding.
type scanner struct {
	bytes      int64
	repo       string
	referenced map[string]bool
}

func (s *scanner) value(v reflect.Value, depth int) error {
	if depth > 64 {
		return fmt.Errorf("scan nesting limit reached")
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return s.value(v.Elem(), depth+1)
		}
	case reflect.String:
		return s.content([]byte(v.String()), depth+1)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return s.content(v.Bytes(), depth+1)
		}
		fallthrough
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if e := s.value(v.Index(i), depth+1); e != nil {
				return e
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" {
				if e := s.value(v.Field(i), depth+1); e != nil {
					return e
				}
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() == reflect.String {
			b, e := json.Marshal(v.Interface())
			if e != nil {
				return e
			}
			var m map[string]any
			if e = json.Unmarshal(b, &m); e != nil {
				return e
			}
			if _, lineage := m["lineage"]; lineage {
				if _, serial := m["serial"]; serial {
					return fmt.Errorf("plaintext state persisted")
				}
			}
			if _, plan := m["planned_values"]; plan {
				return fmt.Errorf("plaintext plan persisted")
			}
			if id, ok := m["jobId"].(string); ok && m["taskOrdinal"] != nil && m["name"] != nil {
				s.referenced[id] = true
			}
		}
		for _, k := range v.MapKeys() {
			if e := s.value(k, depth+1); e != nil {
				return e
			}
			if e := s.value(v.MapIndex(k), depth+1); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *scanner) content(b []byte, depth int) error {
	s.bytes += int64(len(b))
	if s.bytes > 256<<20 || depth > 64 {
		return fmt.Errorf("scan resource limit reached")
	}
	for _, private := range []string{"private-token-sentinel", "private-plan-sentinel", "private-state-sentinel", "private-diagnostic-sentinel", "BEGIN PRIVATE KEY"} {
		if bytes.Contains(b, []byte(private)) {
			return fmt.Errorf("private sentinel in persisted content")
		}
	}
	if len(b) == 0 {
		return nil
	}
	var decoded any
	if json.Unmarshal(b, &decoded) == nil {
		// A JSON string can decode to itself only when the quotes were absent; each
		// successful decoding here removes at least one layer of serialization.
		return s.value(reflect.ValueOf(decoded), depth+1)
	}
	if len(b)%4 == 0 && len(b) > 16 {
		if decoded, e := base64.StdEncoding.DecodeString(string(b)); e == nil {
			if e = s.content(decoded, depth+1); e != nil {
				return e
			}
		}
	}
	if bytes.HasPrefix(b, []byte{0x1f, 0x8b}) {
		r, e := gzip.NewReader(bytes.NewReader(b))
		if e != nil {
			return e
		}
		defer r.Close()
		decoded, e := io.ReadAll(io.LimitReader(r, 256<<20+1))
		if e != nil {
			return e
		}
		return s.content(decoded, depth+1)
	}
	if bytes.HasPrefix(b, []byte("PK\x03\x04")) {
		z, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if e != nil {
			return e
		}
		for _, f := range z.File {
			r, e := f.Open()
			if e != nil {
				return e
			}
			data, e := io.ReadAll(io.LimitReader(r, 256<<20+1))
			ce := r.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
			if e = s.content(data, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if bytes.HasPrefix(b, []byte("PACK")) {
		return s.pack(b, depth+1)
	}
	// Tar includes c2j object bundles. Invalid headers/read errors fail closed once
	// the archive header identifies it; ciphertext itself is intentionally opaque.
	if len(b) > 262 && string(b[257:262]) == "ustar" {
		tr := tar.NewReader(bytes.NewReader(b))
		for {
			h, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			if e = s.content([]byte(h.Name), depth+1); e != nil {
				return e
			}
			data, e := io.ReadAll(io.LimitReader(tr, 256<<20+1))
			if e != nil {
				return e
			}
			if e = s.content(data, depth+1); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *scanner) pack(b []byte, depth int) error {
	root, e := os.MkdirTemp("", "tofu-pack-scan-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(root)
	cmd := exec.Command("git", "init", "--bare", "--quiet", root)
	if out, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("scan pack init: %v %s", e, out)
	}
	env := append(os.Environ(), "GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(s.repo, ".git", "objects"))
	cmd = exec.Command("git", "--git-dir="+root, "index-pack", "--stdin", "--fix-thin")
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(b)
	if out, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("scan pack index: %v %s", e, out)
	}
	cmd = exec.Command("git", "--git-dir="+root, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	cmd.Env = env
	ids, e := cmd.Output()
	if e != nil {
		return e
	}
	for _, id := range strings.Fields(string(ids)) {
		cmd = exec.Command("git", "--git-dir="+root, "cat-file", "-p", id)
		cmd.Env = env
		data, e := cmd.Output()
		if e != nil {
			return e
		}
		if e = s.content(data, depth+1); e != nil {
			return e
		}
	}
	return nil
}
func scanHistory(ctx context.Context, rt jobdb.WorkflowRuntime, key jobdb.JobKey, repo string) (int, int, error) {
	s := scanner{repo: repo, referenced: map[string]bool{key.JobId: true}}
	seen := map[string]bool{}
	chapters, artifacts := 0, 0
	for {
		id := ""
		for candidate := range s.referenced {
			if !seen[candidate] {
				id = candidate
				break
			}
		}
		if id == "" {
			break
		}
		seen[id] = true
		if len(seen) > 100 {
			return 0, 0, fmt.Errorf("scan job limit reached")
		}
		k := jobdb.JobKey{TenantId: key.TenantId, JobId: id}
		info, e := rt.GetJob(ctx, k)
		if e != nil {
			return 0, 0, e
		}
		if e = s.value(reflect.ValueOf(info), 0); e != nil {
			return 0, 0, e
		}
		// JobInfo.Data is a computed final-result accessor (it fails for active
		// or cancelled jobs). Scan its underlying persisted application bytes in
		// the complete chapter collection instead.
		all, e := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: k, StartOrdinal: 0})
		if e != nil {
			return 0, 0, e
		}
		chapters += len(all)
		for _, chapter := range all {
			if e = s.value(reflect.ValueOf(chapter), 0); e != nil {
				return 0, 0, fmt.Errorf("chapter %d: %w", chapter.Ordinal, e)
			}
			if ex, ok := chapter.Body.(jobdb.ExecutionChapter); ok {
				record, e := jobdb.DecodeExecutionRecord(ex.Record)
				if e != nil {
					return 0, 0, e
				}
				if e = s.value(reflect.ValueOf(record), 0); e != nil {
					return 0, 0, e
				}
			}
			for _, stored := range chapter.Artifacts {
				a, e := rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: k, Ordinal: chapter.Ordinal, Name: stored.Name, Digest: stored.Digest})
				if e != nil {
					return 0, 0, e
				}
				r, e := a.Open()
				if e != nil {
					return 0, 0, e
				}
				b, e := io.ReadAll(io.LimitReader(r, 256<<20+1))
				ce := r.Close()
				if e != nil {
					return 0, 0, e
				}
				if ce != nil {
					return 0, 0, ce
				}
				if int64(len(b)) != stored.Size || a.Size() != stored.Size {
					return 0, 0, fmt.Errorf("artifact size mismatch")
				}
				if e = s.content(b, 0); e != nil {
					return 0, 0, fmt.Errorf("artifact %s: %w", stored.Name, e)
				}
				artifacts++
			}
		}
	}
	return chapters, artifacts, nil
}
