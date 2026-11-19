package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	f := newFixture(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "c2ops loopback test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"api.github.com", "uploads.github.com", "github.com", "objects.githubusercontent.com"}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer proxy.Close()
	trust := t.TempDir()
	ca := filepath.Join(trust, "ca.pem")
	if err := os.WriteFile(ca, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary)
			cmd.Dir = cwd
			// Empty PATH proves the installed payload needs no source compiler/tools.
			cmd.Env = []string{"PATH=", "HOME=" + trust, "GITHUB_TOKEN=" + token, "SSL_CERT_FILE=" + ca, "SSL_CERT_DIR=" + trust, "HTTPS_PROXY=" + proxy.URL, "NO_PROXY="}
			wire, _ := json.Marshal(f.in)
			if bad {
				wire = []byte(`{"intent":{}}`)
			}
			cmd.Stdin = bytes.NewReader(wire)
			var out, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diagnostic
			err := cmd.Run()
			if bad {
				if err == nil || out.Len() != 0 || diagnostic.Len() == 0 {
					t.Fatal("invalid installed failure contract")
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
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tag == nil || f.release == nil || f.release.Draft || f.publishCount != 1 || f.uploadCount != len(f.in.Files) {
		t.Fatal("installed process did not complete real HTTP fixture lifecycle")
	}
}
