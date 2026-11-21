package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This test executes the shipped wrapper, with no injected production transport.
// A CONNECT proxy tunnels only the fixed GitHub hosts to a loopback TLS fixture;
// its private CA is trusted only in the child process. No external DNS is used.
func TestC03InstalledPackageFromImmutableCWD(t *testing.T) {
	binary := os.Getenv("C2OPS_GITHUB_INSTALLED_BINARY")
	if binary == "" {
		t.Skip("installed package path not supplied; C03 installed success unverified")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Dir(filepath.Dir(binary))
	if !strings.HasPrefix(cwd, "/nix/store/") {
		t.Fatal("installed test requires actual immutable Nix package")
	}
	info, err := os.Stat(cwd)
	if err != nil || info.Mode().Perm()&0222 != 0 {
		t.Fatal("package cwd is not immutable")
	}
	for _, scenario := range []string{"success", "unrelated-ca", "wrong-hostname", "invalid-input", "invalid-bundle", "directory-fallback"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "invalid-bundle" && runtime.GOOS != "darwin" {
				t.Skip("explicit invalid-bundle rejection is macOS-specific")
			}
			if scenario == "directory-fallback" && runtime.GOOS != "linux" {
				t.Skip("SSL_CERT_DIR fallback is Linux-specific")
			}
			f := newFixture(t)
			names := []string{"api.github.com", "uploads.github.com", "github.com", "objects.githubusercontent.com"}
			if scenario == "wrong-hostname" {
				names = []string{"wrong.example"}
			}
			pair, caPEM := fixtureCertificate(t, names)
			proxy, calls := installedProxy(t, f, pair)
			trust := t.TempDir()
			ca := writeBundle(t, caPEM)
			if scenario == "unrelated-ca" {
				_, unrelated := fixtureCertificate(t, names)
				ca = writeBundle(t, unrelated)
			}
			if scenario == "invalid-bundle" {
				ca = writeBundle(t, []byte(token+" invalid certificate"))
			}
			if scenario == "directory-fallback" {
				if err := os.WriteFile(filepath.Join(trust, "root.pem"), caPEM, 0600); err != nil {
					t.Fatal(err)
				}
				ca = filepath.Join(t.TempDir(), "missing.pem")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary)
			cmd.Dir = cwd
			// Each invocation has fresh trust/proxy caches and an empty PATH.
			cmd.Env = []string{"PATH=", "HOME=" + trust, "GITHUB_TOKEN=" + token, "SSL_CERT_FILE=" + ca, "SSL_CERT_DIR=" + trust, "HTTPS_PROXY=" + proxy, "NO_PROXY="}
			wire, _ := json.Marshal(f.in)
			if scenario == "invalid-input" {
				wire = []byte(`{"intent":{}}`)
			}
			cmd.Stdin = bytes.NewReader(wire)
			var out, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diagnostic
			err := cmd.Run()
			f.mu.Lock()
			defer f.mu.Unlock()
			if scenario != "success" && scenario != "directory-fallback" {
				if err == nil || out.Len() != 0 || diagnostic.Len() == 0 || diagnostic.Len() > 1024 || strings.Contains(diagnostic.String(), token) || strings.Contains(diagnostic.String(), ca) {
					t.Fatalf("invalid installed failure contract: %v %q %q", err, &out, &diagnostic)
				}
				if len(f.writes) != 0 || len(f.reads) != 0 || f.tag != nil || f.release != nil {
					t.Fatal("rejected invocation reached remote HTTP fixture")
				}
				if scenario == "invalid-input" || scenario == "invalid-bundle" {
					if calls.Load() != 0 {
						t.Fatal("configuration/input failure contacted proxy")
					}
				} else if calls.Load() == 0 {
					t.Fatal("TLS rejection did not exercise proxy")
				}
				if scenario == "invalid-bundle" && diagnostic.String() != "github.release: SSL_CERT_FILE must contain a readable PEM certificate bundle\n" {
					t.Fatal("unexpected configuration diagnostic")
				}
				return
			}
			if err != nil || diagnostic.Len() != 0 {
				t.Fatalf("installed execution failed: %v %s", err, &diagnostic)
			}
			var result struct {
				Output Output `json:"output"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Output.Release.Draft || result.Output.Release.Commit != commit || len(result.Output.Release.Assets) != len(f.in.Files) {
				t.Fatal("installed output incomplete")
			}
			for _, asset := range f.in.Files {
				retained, err := os.ReadFile(filepath.Join(f.in.OutboxRoot, asset.Name))
				original, _ := os.ReadFile(filepath.Join(f.in.InboxRoot, asset.Name))
				if err != nil || !bytes.Equal(retained, original) {
					t.Fatal("installed retention mismatch")
				}
			}
			if f.tag == nil || f.release == nil || f.release.Draft || f.publishCount != 1 || f.uploadCount != len(f.in.Files) {
				t.Fatal("installed process did not complete real HTTP fixture lifecycle")
			}
		})
	}
}

func installedProxy(t *testing.T, f *fixture, pair tls.Certificate) (string, *atomic.Int32) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	calls := new(atomic.Int32)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		allowed := map[string]bool{"api.github.com:443": true, "uploads.github.com:443": true, "github.com:443": true, "objects.githubusercontent.com:443": true}
		if r.Method != "CONNECT" || !allowed[r.Host] {
			http.Error(w, "fixture destination denied", 403)
			return
		}
		upstream, e := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
		if e != nil {
			http.Error(w, "fixture unavailable", 502)
			return
		}
		conn, buffer, e := w.(http.Hijacker).Hijack()
		if e != nil {
			upstream.Close()
			return
		}
		defer conn.Close()
		defer upstream.Close()
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		_ = upstream.SetDeadline(time.Now().Add(30 * time.Second))
		_, _ = fmt.Fprint(buffer, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, buffer); _ = upstream.Close(); close(done) }()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	return proxy.URL, calls
}
