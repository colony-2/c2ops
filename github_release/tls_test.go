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
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A private root signs a separate server certificate. Every call creates an
// independent authority, so unrelated-root controls cannot share trust.
func fixtureCertificate(t *testing.T, names []string) (tls.Certificate, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "c2ops loopback CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, root, root, public, private)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	root, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	leafPublic, leafPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: names}
	der, err = x509.CreateCertificate(rand.Reader, leaf, root, leafPublic, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(leafPrivate)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	if err != nil {
		t.Fatal(err)
	}
	return pair, ca
}

func writeBundle(t *testing.T, bundle []byte) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestMacOSBundleTrust(t *testing.T) {
	pair, ca := fixtureCertificate(t, []string{"api.github.com"})
	_, unrelated := fixtureCertificate(t, []string{"api.github.com"})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "trusted") }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	for _, test := range []struct {
		name, hostname string
		bundle         []byte
		wantSuccess    bool
	}{
		{"single-root", "api.github.com", ca, true},
		{"second-root", "api.github.com", append(append([]byte(nil), unrelated...), ca...), true},
		{"unrelated-root", "api.github.com", unrelated, false},
		{"wrong-hostname", "wrong.example", ca, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper, err := githubTransport("darwin", writeBundle(t, test.bundle))
			if err != nil {
				t.Fatal(err)
			}
			transport := roundTripper.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			transport.Proxy = nil
			if transport.TLSClientConfig == nil {
				transport.TLSClientConfig = &tls.Config{}
			}
			transport.TLSClientConfig.ServerName = test.hostname
			if transport.TLSClientConfig.InsecureSkipVerify {
				t.Fatal("certificate verification disabled")
			}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			response, err := client.Get(server.URL)
			if response != nil {
				response.Body.Close()
			}
			if (err == nil) != test.wantSuccess {
				t.Fatalf("TLS success = %v, want %v: %v", err == nil, test.wantSuccess, err)
			}
		})
	}
}

func TestBundleSelectionAndErrors(t *testing.T) {
	for _, test := range []struct{ goos, file string }{
		{"darwin", ""}, {"linux", ""}, {"linux", "/missing/ignored-by-helper"},
	} {
		transport, err := githubTransport(test.goos, test.file)
		if err != nil || transport != http.DefaultTransport {
			t.Fatalf("native trust selection changed for %v: %v", test, err)
		}
	}
	for name, file := range map[string]string{
		"missing": filepath.Join(t.TempDir(), token),
		// Opening a directory fails deterministically even as root.
		"unreadable": t.TempDir(),
		"empty":      writeBundle(t, nil),
		"invalid":    writeBundle(t, []byte(token+"\n-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----")),
	} {
		t.Run(name, func(t *testing.T) {
			transport, err := githubTransport("darwin", file)
			if transport != nil || err == nil || err.Error() != "SSL_CERT_FILE must contain a readable PEM certificate bundle" {
				t.Fatalf("unexpected configuration result: %v %v", transport, err)
			}
		})
	}
}

func TestBundleTransportIsolation(t *testing.T) {
	original := http.DefaultTransport
	base := original.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: x509.NewCertPool()}
	proxy, _ := url.Parse("http://proxy.example:8080")
	base.Proxy = http.ProxyURL(proxy)
	base.ResponseHeaderTimeout = 17 * time.Second
	http.DefaultTransport = base
	t.Cleanup(func() { http.DefaultTransport = original; base.CloseIdleConnections() })
	_, ca := fixtureCertificate(t, []string{"api.github.com"})
	roundTripper, err := githubTransport("darwin", writeBundle(t, ca))
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripper.(*http.Transport)
	defer transport.CloseIdleConnections()
	selectedProxy, _ := transport.Proxy(&http.Request{})
	if selectedProxy.String() != proxy.String() || transport.ResponseHeaderTimeout != base.ResponseHeaderTimeout || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatal("connection/proxy/TLS settings lost")
	}
	if http.DefaultTransport != base || transport == base || transport.TLSClientConfig == base.TLSClientConfig || transport.TLSClientConfig.RootCAs == base.TLSClientConfig.RootCAs || len(base.TLSClientConfig.RootCAs.Subjects()) != 0 {
		t.Fatal("global transport or TLS trust mutated")
	}
}

func TestMacOSBundleFailureBeforeRequests(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("constructor uses actual host; native macOS required")
	}
	f := newFixture(t)
	var calls atomic.Int32
	base := http.DefaultTransport
	transport := base.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) { calls.Add(1); return nil, nil }
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = base; transport.CloseIdleConnections() })
	t.Setenv("SSL_CERT_FILE", writeBundle(t, []byte(token+" invalid certificate contents")))
	wire, _ := json.Marshal(f.in)
	var out bytes.Buffer
	err := execute(context.Background(), bytes.NewReader(wire), &out, token, nil)
	if err == nil || out.Len() != 0 || calls.Load() != 0 || err.Error() != "SSL_CERT_FILE must contain a readable PEM certificate bundle" || strings.Contains(err.Error(), token) {
		t.Fatalf("configuration failure contract: stdout=%q requests=%d error=%v", out.String(), calls.Load(), err)
	}
}
