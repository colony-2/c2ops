package jobexec

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/golang-jwt/jwt/v5"
	"github.com/julienschmidt/httprouter"
	"github.com/nektos/act/pkg/artifacts"
)

// artifactsBridge reuses act's standard v4 protocol. The server is private to a
// job; completed dependency snapshots are imported, and new uploads are sealed
// into an object outbox directory when execution ends.
type artifactsBridge struct {
	server       *http.Server
	listener     net.Listener
	token        string
	root, output string
	inherited    map[string]bool
	finalized    map[string]bool
	mu           sync.Mutex
}

func startArtifacts(ctx context.Context, root, output string, inputs []string) (*artifactsBridge, error) {
	b := &artifactsBridge{root: root, output: output, inherited: map[string]bool{}, finalized: map[string]bool{}}
	if err := os.MkdirAll(filepath.Join(root, "1"), 0700); err != nil {
		return nil, err
	}
	for _, input := range inputs {
		entries, err := os.ReadDir(input)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !safeArtifactName(entry.Name()) {
				return nil, fmt.Errorf("invalid artifact snapshot")
			}
			dst := filepath.Join(root, "1", entry.Name())
			if err := copyArtifactTree(filepath.Join(input, entry.Name()), dst); err != nil {
				return nil, err
			}
			b.inherited[entry.Name()] = true
		}
	}
	var err error
	// act validates the JWT shape; the adapter additionally compares the exact
	// random capability. No fixed credential expiry imposes a job time limit.
	b.token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"scp": "Actions.Results:1:1", "TaskID": 1, "RunID": 1, "JobID": 1,
		"jti": rand.Text(), "ac": `[{"Scope":"","Permission":2}]`,
	}).SignedString([]byte{})
	if err != nil {
		return nil, err
	}
	b.listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	router := httprouter.New()
	files := artifactFS{root: root}
	artifacts.RoutesV4(router, root, files, files)
	b.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// act's v4 routes mutate shared AppURL. Serialize requests to avoid that
		// race, and to make final snapshot publication a clear boundary.
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method == http.MethodPost && r.Header.Get("Authorization") != "Bearer "+b.token {
			http.Error(w, "unauthorized", 401)
			return
		}
		finalizedName := ""
		if r.Method == http.MethodPost {
			data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			if strings.HasSuffix(r.URL.Path, "/FinalizeArtifact") {
				finalizedName = artifactRequestName(data)
				if !safeArtifactName(finalizedName) || b.inherited[finalizedName] {
					http.Error(w, "invalid artifact", 400)
					return
				}
				archive, err := zip.OpenReader(filepath.Join(root, "1", finalizedName, finalizedName+".zip"))
				if err != nil {
					http.Error(w, "artifact is not a complete ZIP", 400)
					return
				}
				archive.Close()
			}
			// Reject duplicate names before act's create route truncates an upload.
			if strings.HasSuffix(r.URL.Path, "/CreateArtifact") || strings.HasSuffix(r.URL.Path, "/DeleteArtifact") {
				name := artifactRequestName(data)
				if !safeArtifactName(name) {
					http.Error(w, "invalid artifact name", 400)
					return
				}
				_, err := os.Stat(filepath.Join(root, "1", name))
				if b.inherited[name] || strings.HasSuffix(r.URL.Path, "/CreateArtifact") && err == nil {
					http.Error(w, "artifact already exists; names must be unique across jobs", 409)
					return
				}
			}
		}
		if name := r.URL.Query().Get("artifactName"); name != "" {
			if !safeArtifactName(name) {
				http.Error(w, "invalid artifact name", 400)
				return
			}
			if r.Method == http.MethodPut && (b.inherited[name] || b.finalized[name]) {
				http.Error(w, "artifact is immutable", 409)
				return
			}
		}
		recorded := &artifactResponse{ResponseWriter: w, status: 200}
		router.ServeHTTP(recorded, r)
		if finalizedName != "" && recorded.status >= 200 && recorded.status < 300 {
			b.finalized[finalizedName] = true
		}
	})}
	go func() { _ = b.server.Serve(b.listener) }()
	return b, nil
}
func (b *artifactsBridge) env() map[string]string {
	return map[string]string{"ACTIONS_RUNTIME_URL": "http://" + b.listener.Addr().String() + "/", "ACTIONS_RESULTS_URL": "http://" + b.listener.Addr().String() + "/", "ACTIONS_RUNTIME_TOKEN": b.token, "GITHUB_RUN_ID": "1"}
}
func (b *artifactsBridge) close() error {
	// No requests can write files after publication begins. Close also handles
	// cancellation, without adding a workflow timeout.
	if err := b.server.Close(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := os.MkdirAll(b.output, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(b.root, "1"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if b.inherited[entry.Name()] || !b.finalized[entry.Name()] {
			continue
		}
		if !entry.IsDir() {
			return fmt.Errorf("unexpected artifact state")
		}
		if err := copyArtifactTree(filepath.Join(b.root, "1", entry.Name()), filepath.Join(b.output, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
func safeArtifactName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00\r\n")
}
func copyArtifactTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact snapshot contains a non-regular file")
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		defer source.Close()
		if old, err := os.Open(target); err == nil {
			defer old.Close()
			a, b := sha256.New(), sha256.New()
			if _, err = io.Copy(a, source); err != nil {
				return err
			}
			if _, err = io.Copy(b, old); err != nil {
				return err
			}
			if !bytes.Equal(a.Sum(nil), b.Sum(nil)) {
				return fmt.Errorf("conflicting dependency artifact %q", entry.Name())
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(output, source)
		closeErr := output.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

type artifactFS struct{ root string }

func (f artifactFS) check(name string) error {
	rel, err := filepath.Rel(f.root, name)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("artifact path escapes store")
	}
	return nil
}
func (f artifactFS) Open(name string) (fs.File, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	return os.Open(name)
}
func (f artifactFS) OpenWritable(name string) (artifacts.WritableFile, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
}
func (f artifactFS) OpenAppendable(name string) (artifacts.WritableFile, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	return os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}

func artifactRequestName(data []byte) string {
	var v struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(data, &v)
	return v.Name
}

type artifactResponse struct {
	http.ResponseWriter
	status int
}

func (w *artifactResponse) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
