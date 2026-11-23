package main

// This fixture drives real OpenTofu and Google 7.10.0. Only the test dependency
// adds a private HTTPS proxy/CA and an offline provider mirror. The production
// executable has no such inputs or environment switches.
import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"

	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fixtureReporter interface {
	Error(...any)
	Errorf(string, ...any)
}

type gcsFixture struct {
	storage http.Handler
	sync.Mutex
	objects                                                  map[string][]byte
	generation                                               int
	failWrites                                               bool
	requests, locks, tokenRequests, impersonations, projects int
	auth                                                     string
	t                                                        fixtureReporter
	failFlag                                                 string
	blockFlag, blockReady                                    string
	key                                                      *rsa.PublicKey
	tokens                                                   map[string]fixtureToken
	stage                                                    int
	checkRenewal                                             bool
	renewal                                                  map[string]*renewalProbe
}

type fixtureToken struct {
	issued, expires time.Time
	target          bool
	parent          string
}
type renewalProbe struct {
	stage          int
	request, token string
	expiry         time.Time
	passed         bool
	sourceRenewed  bool
}

func verifyAssertion(assertion string, key *rsa.PublicKey, now time.Time) error {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 || key == nil {
		return fmt.Errorf("missing JWT/key")
	}
	header, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return e
	}
	var h struct{ Alg, Typ string }
	if json.Unmarshal(header, &h) != nil || h.Alg != "RS256" || h.Typ != "JWT" {
		return fmt.Errorf("invalid JWT header")
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil {
		return e
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if e = rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); e != nil {
		return e
	}
	body, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return e
	}
	var c struct {
		Iss, Sub, Aud, Scope string
		Iat, Exp             int64
	}
	if json.Unmarshal(body, &c) != nil || c.Iss != "fixture@fixture.iam.gserviceaccount.com" || (c.Sub != "" && c.Sub != c.Iss) || c.Aud != "https://oauth2.googleapis.com/token" || c.Iat < now.Add(-time.Minute).Unix() || c.Iat > now.Add(time.Minute).Unix() || c.Exp <= now.Unix() || c.Exp <= c.Iat || c.Exp-c.Iat > 3600 {
		return fmt.Errorf("invalid JWT claims")
	}
	scopes := strings.Fields(c.Scope)
	if len(scopes) == 0 {
		return fmt.Errorf("missing JWT scope")
	}
	for _, scope := range scopes {
		if !strings.HasPrefix(scope, "https://www.googleapis.com/auth/") {
			return fmt.Errorf("invalid JWT scope")
		}
	}
	return nil
}

func (f *gcsFixture) authorized(r *http.Request, source bool) bool {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if f.key == nil {
		return r.Header.Get("Authorization") == "Bearer "+f.auth
	}
	tok, ok := f.tokens[auth]
	return ok && time.Now().Before(tok.expires) && tok.target == (!source && f.auth == "fixture-target")
}

func (f *gcsFixture) issueToken(target bool) string {
	if f.tokens == nil {
		f.tokens = map[string]fixtureToken{}
	}
	name := fmt.Sprintf("fixture-token-%d", len(f.tokens)+1)
	now := time.Now()
	f.tokens[name] = fixtureToken{issued: now, expires: now.Add(2 * time.Second), target: target}
	return name
}

func (f *gcsFixture) probeRenewal(w http.ResponseWriter, r *http.Request, client string) bool {
	if !f.checkRenewal {
		return false
	}
	if f.renewal == nil {
		f.renewal = map[string]*renewalProbe{}
	}
	name := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	token := f.tokens[name]
	request := r.Method + " " + r.URL.String()
	p, ok := f.renewal[client]
	if !ok {
		f.renewal[client] = &renewalProbe{stage: f.stage, request: request, token: name, expiry: token.expires}
		// The same SDK request must retry in this same OpenTofu process after
		// the token's actual expiry. New clients in later stages cannot pass.
		f.Unlock()
		time.Sleep(time.Until(token.expires) + 200*time.Millisecond)
		f.Lock()
		http.Error(w, `{"error":{"code":503,"message":"retry renewal probe"}}`, 503)
		return true
	}
	if p.stage == f.stage && p.request == request && name != p.token && !token.issued.Before(p.expiry) {
		p.passed = true
		if token.target {
			oldSource := f.tokens[f.tokens[p.token].parent]
			newSource := f.tokens[token.parent]
			p.sourceRenewed = token.parent != f.tokens[p.token].parent && !newSource.issued.Before(oldSource.expires)
		}
	}
	return false
}

func (f *gcsFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()
	f.requests++
	w.Header().Set("Content-Type", "application/json")
	if r.Host == "oauth2.googleapis.com" {
		f.tokenRequests++
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || verifyAssertion(r.Form.Get("assertion"), f.key, time.Now()) != nil {
			f.t.Error("JWT signature or claims invalid")
			http.Error(w, "bad auth", 400)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":2}`, f.issueToken(false))
		return
	}
	if strings.Contains(r.Host, "iamcredentials.googleapis.com") {
		f.impersonations++
		if !f.authorized(r, true) || r.URL.Path != "/v1/projects/-/serviceAccounts/target@fixture.iam.gserviceaccount.com:generateAccessToken" {
			f.t.Error("wrong impersonation source or target")
			http.Error(w, "bad impersonation source or target", http.StatusBadRequest)
			return
		}
		body, e := io.ReadAll(r.Body)
		var claims struct {
			Scope     []string
			Lifetime  string
			Delegates []string
		}
		decodeErr := json.Unmarshal(body, &claims)
		if e != nil || decodeErr != nil || len(claims.Scope) == 0 || len(claims.Delegates) != 0 {
			f.t.Errorf("invalid impersonation claims: stage=%d method=%s bytes=%d content_length=%d read=%v decode=%v scopes=%d delegates=%d", f.stage, r.Method, len(body), r.ContentLength, e, decodeErr, len(claims.Scope), len(claims.Delegates))
			http.Error(w, "bad impersonation claims", http.StatusBadRequest)
			return
		}
		name := f.issueToken(true)
		token := f.tokens[name]
		token.parent = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.tokens[name] = token
		fmt.Fprintf(w, `{"accessToken":%q,"expireTime":%q}`, name, f.tokens[name].expires.UTC().Format(time.RFC3339Nano))
		return
	}
	if r.Host == "openidconnect.googleapis.com" {
		if !f.authorized(r, false) && !f.authorized(r, true) {
			f.t.Error("wrong userinfo auth")
		}
		io.WriteString(w, `{"email":"fixture@fixture.iam.gserviceaccount.com"}`)
		return
	}
	if r.Host == "cloudbilling.googleapis.com" {
		if !f.authorized(r, false) {
			f.t.Error("wrong billing auth")
		}
		io.WriteString(w, `{"name":"projects/fixture-project/billingInfo","projectId":"fixture-project","billingEnabled":false}`)
		return
	}
	if !f.authorized(r, false) {
		f.t.Errorf("unexpected auth at %s", r.Host)
		http.Error(w, "bad auth", 403)
		return
	}
	if strings.Contains(r.Host, "cloudresourcemanager.googleapis.com") {
		if f.probeRenewal(w, r, "provider") {
			return
		}
		f.projects++
		io.WriteString(w, `{"projectId":"fixture-project","projectNumber":"123456789","name":"fixture-project","lifecycleState":"ACTIVE"}`)
		return
	}
	if r.Host != "storage.googleapis.com" {
		f.t.Errorf("unexpected fixture service %s %s", r.Host, r.URL.Path)
		http.Error(w, "unsupported", 400)
		return
	}
	if r.Method == "GET" && f.probeRenewal(w, r, "backend") {
		return
	}
	if r.Method == "GET" && filePresent(f.blockFlag) {
		if e := os.WriteFile(f.blockReady, []byte("real Google client entered plan"), 0600); e != nil {
			f.t.Error(e)
		}
		// The real SDK request stays pending until operation cancellation. Do
		// not hold the fixture mutex while waiting for that request to end.
		f.Unlock()
		select {
		case <-r.Context().Done():
		case <-time.After(15 * time.Second):
		}
		f.Lock()
		return
	}
	if f.storage != nil {
		f.storage.ServeHTTP(w, r)
		return
	}
	p := r.URL.Path
	if r.Method == "GET" && strings.HasSuffix(p, "/o") {
		io.WriteString(w, `{"kind":"storage#objects","items":[]}`)
		return
	}
	name := ""
	if pos := strings.Index(p, "/o/"); pos >= 0 {
		name = p[pos+3:]
	} else if strings.HasPrefix(p, "/fixture-bucket/") {
		name = strings.TrimPrefix(p, "/fixture-bucket/")
	}
	if r.Method == "POST" && strings.Contains(p, "/upload/") {
		name = r.URL.Query().Get("name")
		var data []byte
		typ, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if strings.HasPrefix(typ, "multipart/") {
			mr := multipart.NewReader(r.Body, params["boundary"])
			part, e := mr.NextPart()
			if e != nil {
				f.t.Error(e)
				return
			}
			meta, _ := io.ReadAll(part)
			var m struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(meta, &m)
			if m.Name != "" {
				name = m.Name
			}
			part, e = mr.NextPart()
			if e != nil {
				f.t.Error(e)
				return
			}
			data, _ = io.ReadAll(part)
		} else {
			data, _ = io.ReadAll(r.Body)
		}
		if name == "" {
			f.t.Errorf("upload without name: %s", r.URL)
			http.Error(w, "bad request", 400)
			return
		}
		if strings.HasSuffix(name, ".tfstate") && (f.failWrites || filePresent(f.failFlag)) {
			http.Error(w, `{"error":{"code":403,"message":"fixture persistence denied"}}`, 403)
			return
		}
		if strings.HasSuffix(name, ".tflock") {
			if _, ok := f.objects[name]; ok {
				http.Error(w, `{"error":{"code":412}}`, 412)
				return
			}
			f.locks++
		}
		f.objects[name] = data
		f.generation++
		fmt.Fprintf(w, `{"kind":"storage#object","name":%q,"bucket":"fixture-bucket","generation":%q,"size":%q}`, name, strconv.Itoa(f.generation), strconv.Itoa(len(data)))
		return
	}
	data, ok := f.objects[name]
	if r.Method == "DELETE" {
		if !ok {
			http.Error(w, `{"error":{"code":404}}`, 404)
			return
		}
		delete(f.objects, name)
		w.WriteHeader(204)
		return
	}
	if !ok {
		http.Error(w, `{"error":{"code":404,"message":"not found"}}`, 404)
		return
	}
	if r.URL.Query().Get("alt") == "media" || strings.HasPrefix(p, "/fixture-bucket/") {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Goog-Generation", strconv.Itoa(f.generation))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
		return
	}
	fmt.Fprintf(w, `{"kind":"storage#object","name":%q,"bucket":"fixture-bucket","generation":%q,"size":%q}`, name, strconv.Itoa(f.generation), strconv.Itoa(len(data)))
}

// A Keychain helper can retain sudo's output descriptors after sudo exits.
// Give it a regular file so command completion depends only on the exit status.
func runFixtureSecurity(cmd *exec.Cmd) error {
	output, err := os.CreateTemp("", "opentofu-fixture-security-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	defer output.Close()
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		diagnostic, readErr := io.ReadAll(io.NewSectionReader(output, 0, 8192))
		if readErr != nil {
			return fmt.Errorf("%w (reading diagnostic: %v)", err, readErr)
		}
		return fmt.Errorf("%w: %s", err, diagnostic)
	}
	return nil
}

// The released macOS provider uses Keychain trust. Install only this short-lived
// server certificate; its private key stays in the test process.
func trustFixtureLeaf(t *testing.T, path string, der []byte) {
	t.Helper()
	const keychain = "/Library/Keychains/System.keychain"
	security := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/usr/bin/sudo", append([]string{"-n", "/usr/bin/security"}, args...)...)
		// Give sudo a chance to forward termination to the privileged command.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = time.Second
		if err := runFixtureSecurity(cmd); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("security %s: %w (%v)", args[0], ctx.Err(), err)
			}
			return fmt.Errorf("security %s: %w", args[0], err)
		}
		return nil
	}
	t.Cleanup(func() {
		if err := security("remove-trusted-cert", "-d", path); err != nil {
			t.Error(err)
		}
		if err := security("delete-certificate", "-Z", fmt.Sprintf("%X", sha256.Sum256(der)), keychain); err != nil {
			t.Error(err)
		}
	})
	if err := security("add-trusted-cert", "-d", "-r", "trustRoot", "-p", "ssl", "-k", keychain, path); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{DNSName: "storage.googleapis.com"}); err != nil {
		t.Fatalf("fixture certificate rejected by the system verifier: %v", err)
	}
}

func googleProxy(t *testing.T, h http.Handler, useSystemTrust bool) (string, string) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{
		Subject:   pkix.Name{CommonName: "Private Google fixture"},
		DNSNames:  fixtureGoogleHosts,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if e = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	if useSystemTrust && runtime.GOOS == "darwin" && os.Getenv("C2OPS_TEST_MACOS_FIXTURE_TRUST") == "1" {
		trustFixtureLeaf(t, ca, der)
	}
	var handlers sync.WaitGroup
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		if r.Method != "CONNECT" {
			t.Error("expected CONNECT")
			http.Error(w, "bad proxy", 400)
			return
		}
		conn, _, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		if tc.Handshake() != nil {
			return
		}
		req, e := http.ReadRequest(bufio.NewReader(tc))
		if e != nil {
			return
		}
		defer req.Body.Close()
		rr := httptest.NewRecorder()
		// The provider SDK refreshes credentials asynchronously and can exit
		// after sending headers but before sending the body. Reject incomplete
		// HTTP messages at the transport boundary; they are not invalid claims
		// and must never mint a fixture token. Complete malformed messages still
		// reach the strict oracle. Preserve EOF diagnostics without request data.
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Logf("fixture transport rejected incomplete %s request to %s: bytes=%d content_length=%d read=%v", req.Method, req.Host, len(body), req.ContentLength, readErr)
			http.Error(rr, "incomplete fixture request", http.StatusBadRequest)
		} else {
			req.Body = io.NopCloser(bytes.NewReader(body))
			h.ServeHTTP(rr, req)
		}
		response := rr.Result()
		defer response.Body.Close()
		response.Close = true
		_ = response.Write(tc)
	}))
	t.Cleanup(func() {
		proxy.Close()
		// net/http does not join hijacked CONNECT connections on Close.
		// Finish their bounded reads before the fixture/subtest is torn down.
		handlers.Wait()
	})
	return proxy.URL, ca
}
func actualTools(t *testing.T) (string, string) {
	t.Helper()
	tofu := os.Getenv("C2OPS_TEST_TOFU")
	zip := os.Getenv("C2OPS_TEST_GOOGLE_ZIP")
	if tofu == "" || zip == "" {
		t.Skip("real-tool qualification requires C2OPS_TEST_TOFU and C2OPS_TEST_GOOGLE_ZIP")
	}
	return tofu, zip
}
func realFixture(t *testing.T) (Input, string, string) {
	t.Helper()
	tofu, zip := actualTools(t)
	i, _ := fixture(t)
	b, e := os.ReadFile("testdata/google-7.10.0.sha256")
	if e != nil {
		t.Fatal(e)
	}
	expected := ""
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		fields := strings.Fields(line)
		if strings.HasSuffix(fields[1], "_"+runtime.GOOS+"_"+runtime.GOARCH+".zip") {
			expected = fields[0]
		}
	}
	zb, e := os.ReadFile(zip)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(zb)
	if hex.EncodeToString(sum[:]) != expected {
		t.Fatal("provider zip checksum mismatch")
	}

	lockBytes, e := os.ReadFile("testdata/.terraform.lock.hcl")
	if e != nil {
		t.Fatal(e)
	}
	lock := string(lockBytes)

	config := `terraform {
 backend "gcs" {}
 required_providers {
  google = { source = "registry.opentofu.org/hashicorp/google", version = "7.10.0" }
 }
}
provider "google" { project = "fixture-project" }
data "google_project" "fixture" { project_id = "fixture-project" }
resource "terraform_data" "fixture" { input = data.google_project.fixture.project_id }
`
	write(t, filepath.Join(i.Worktree, "main.tf"), config)
	write(t, filepath.Join(i.Worktree, ".terraform.lock.hcl"), lock)
	gitTest(t, i.Worktree, "add", ".")
	gitTest(t, i.Worktree, "commit", "-qm", "real Google fixture")
	i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
	mirror := filepath.Join(t.TempDir(), "mirror")
	providerDir := filepath.Join(mirror, "registry.opentofu.org", "hashicorp", "google")
	if e = makePrivate(providerDir); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(providerDir, "terraform-provider-google_7.10.0_"+runtime.GOOS+"_"+runtime.GOARCH+".zip"), string(zb))
	cli := filepath.Join(t.TempDir(), "tofurc")
	write(t, cli, fmt.Sprintf("provider_installation {\n filesystem_mirror { path = %q }\n}\n", mirror))
	return i, tofu, cli
}
func TestRealOpenTofuGoogleBoundary(t *testing.T) {
	for _, mode := range []string{"oauth_access_token", "service_account_json", "impersonation"} {
		t.Run(mode, func(t *testing.T) {
			i, tofu, cli := realFixture(t)
			gcs := &gcsFixture{objects: map[string][]byte{}, auth: "private-token-sentinel", t: t}
			proxy, ca := googleProxy(t, gcs, true)
			now := time.Now()
			secret := validSecret(now)
			if mode != "oauth_access_token" {
				i.Auth = Auth{Mode: "service_account_json", SecretName: "fixture_token", Expected: "fixture@fixture.iam.gserviceaccount.com"}
				key, e := rsa.GenerateKey(rand.Reader, 2048)
				if e != nil {
					t.Fatal(e)
				}
				gcs.key = &key.PublicKey
				gcs.checkRenewal = true
				der, e := x509.MarshalPKCS8PrivateKey(key)
				if e != nil {
					t.Fatal(e)
				}
				bundle := map[string]string{"type": "service_account", "project_id": "fixture", "private_key_id": "fixture-id", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "client_email": i.Auth.Expected, "client_id": "123", "token_uri": "https://oauth2.googleapis.com/token"}
				secret, _ = json.Marshal(bundle)
				gcs.auth = "fixture-source"
				if mode == "impersonation" {
					i.Auth.Impersonate = "target@fixture.iam.gserviceaccount.com"
					gcs.auth = "fixture-target"
				}
			}
			reads := 0
			d := dependencies{now: func() time.Time { return now }, outbox: t.TempDir(), secret: func(context.Context, string) ([]byte, error) { reads++; return append([]byte(nil), secret...), nil }}
			d.run = func(ctx context.Context, c command) runResult {
				gcs.Lock()
				gcs.stage++
				gcs.Unlock()
				c.Path = tofu
				c.CaptureDiagnostics = true
				c.Env = append(c.Env, "HTTPS_PROXY="+proxy, "SSL_CERT_FILE="+ca, "TF_CLI_CONFIG_FILE="+cli)
				r := runProcess(ctx, c)
				if !successful(r) && !(c.Args[0] == "plan" && r.Code != nil && *r.Code == 2) {
					t.Logf("real stage %s exit=%v output=%s", c.Args[0], r.Code, r.Data)
				}
				return r
			}
			env, e := execute(context.Background(), i, d)
			if e != nil || !env.Output.OK || env.Output.Outcome != "applied" {
				t.Fatalf("real apply: %+v %v", env.Output, e)
			}
			env, e = execute(context.Background(), i, d)
			if e != nil || !env.Output.OK || env.Output.Outcome != "no_changes" {
				t.Fatalf("real no change: %+v %v", env.Output, e)
			}
			// A changed preview uses a fresh immutable revision and never applies.
			configPath := filepath.Join(i.Worktree, "main.tf")
			configBytes, e := os.ReadFile(configPath)
			if e != nil {
				t.Fatal(e)
			}
			write(t, configPath, strings.Replace(string(configBytes), "input = data.google_project.fixture.project_id", `input = "preview-only"`, 1))
			gitTest(t, i.Worktree, "add", ".")
			gitTest(t, i.Worktree, "commit", "-qm", "changed preview")
			i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
			i.Mode = "plan"
			env, e = execute(context.Background(), i, d)
			if e != nil || !env.Output.OK || env.Output.Outcome != "planned" {
				t.Fatalf("real changed preview: %+v %v", env.Output, e)
			}
			gcs.Lock()
			defer gcs.Unlock()
			if reads != 3 || gcs.locks < 3 || gcs.projects < 2 {
				t.Fatalf("missing real boundaries: reads=%d locks=%d projects=%d", reads, gcs.locks, gcs.projects)
			}
			if mode != "oauth_access_token" {
				for _, client := range []string{"backend", "provider"} {
					p := gcs.renewal[client]
					if p == nil || !p.passed {
						t.Fatalf("%s did not renew after actual expiry within one stage/client retry", client)
					}
					if mode == "impersonation" && !p.sourceRenewed {
						t.Fatalf("%s target renewal did not also renew its expired source credential", client)
					}
					t.Logf("%s renewed after expiry in stage %d on identical SDK request", client, p.stage)
				}
			}
		})
	}
}

func filePresent(p string) bool {
	if p == "" {
		return false
	}
	_, e := os.Stat(p)
	return e == nil
}

func TestRealStalePlanAndLockTimeout(t *testing.T) {
	for _, kind := range []string{"stale", "lock"} {
		t.Run(kind, func(t *testing.T) {
			i, tofu, cli := realFixture(t)
			i.lock = time.Second
			i.LockTimeout = "1s"
			gcs := &gcsFixture{objects: map[string][]byte{}, auth: "private-token-sentinel", t: t}
			proxy, ca := googleProxy(t, gcs, true)
			reads := 0
			calls := []string{}
			d := dependencies{now: time.Now, outbox: t.TempDir(), secret: func(context.Context, string) ([]byte, error) { reads++; return validSecret(time.Now()), nil }}
			d.run = func(ctx context.Context, c command) runResult {
				stage := c.Args[0]
				calls = append(calls, stage)
				if (kind == "stale" && stage == "apply") || (kind == "lock" && stage == "plan") {
					gcs.Lock()
					if kind == "stale" {
						for name, b := range gcs.objects {
							if strings.HasSuffix(name, ".tfstate") {
								var v map[string]any
								_ = json.Unmarshal(b, &v)
								v["serial"] = 99
								gcs.objects[name], _ = json.Marshal(v)
							}
						}
					} else {
						gcs.objects["fixture/state/default.tflock"] = []byte(`{"ID":"1","Operation":"fixture","Who":"fixture","Version":"1.13.1","Created":"2026-01-01T00:00:00Z","Path":"fixture"}`)
					}
					gcs.Unlock()
				}
				c.Path = tofu
				c.Env = append(c.Env, "HTTPS_PROXY="+proxy, "SSL_CERT_FILE="+ca, "TF_CLI_CONFIG_FILE="+cli)
				return runProcess(ctx, c)
			}
			env, e := execute(context.Background(), i, d)
			want := "apply"
			if kind == "lock" {
				want = "plan"
			}
			if e != nil || env.Output.OK || env.Output.Stage != want || calls[len(calls)-1] != want || reads != 1 {
				t.Fatalf("%s: %v %+v %v", kind, e, env.Output, calls)
			}
		})
	}
}
