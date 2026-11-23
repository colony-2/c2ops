package main

// Only this test transport connects to the pinned, separately built emulator.
import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type localGCS struct {
	endpoint string
	client   *http.Client
	handler  http.Handler
}

func startEmulator(t *testing.T, f *gcsFixture) *localGCS {
	t.Helper()
	binary := os.Getenv("C2OPS_TEST_GCS_EMULATOR")
	if binary == "" {
		t.Fatal("pinned emulator executable not prepared")
	}
	cmd := exec.Command(binary)
	input, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Wait() })
	lines := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(output)
		if s.Scan() {
			lines <- s.Text()
		} else {
			lines <- ""
		}
	}()
	var endpoint string
	select {
	case endpoint = <-lines:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("emulator startup timeout")
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "http" || !strings.HasPrefix(u.Host, "127.0.0.1:") {
		t.Fatal("invalid private emulator endpoint")
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	em := &localGCS{endpoint: endpoint, client: &http.Client{Transport: transport, Timeout: 15 * time.Second}}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = transport
	em.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gcsFixture holds its mutex while this handler runs.
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/upload/") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "invalid body", 400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			name := r.URL.Query().Get("name")
			typ, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err == nil && strings.HasPrefix(typ, "multipart/") {
				part, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).NextPart()
				if err != nil {
					http.Error(w, "invalid upload metadata", 400)
					return
				}
				var metadata struct {
					Name string `json:"name"`
				}
				if json.NewDecoder(part).Decode(&metadata) != nil {
					http.Error(w, "invalid upload metadata", 400)
					return
				}
				name = metadata.Name
			}
			if strings.HasSuffix(name, ".tflock") {
				if r.URL.Query().Get("ifGenerationMatch") != "0" {
					http.Error(w, "missing lock precondition", 400)
					return
				}
				f.locks++
			}
			if strings.HasSuffix(name, ".tfstate") && (f.failWrites || filePresent(f.failFlag)) {
				marker, _, status := em.object("fixture/marker")
				if status == 200 && string(marker) == "opentofu integration marker" {
					t.Log("emulator rejected state persistence after marker effect")
					http.Error(w, `{"error":{"code":403,"message":"local persistence fault"}}`, 403)
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	})
	return em
}
func (e *localGCS) object(name string) ([]byte, string, int) {
	req, _ := http.NewRequest("GET", e.endpoint+"/storage/v1/b/fixture-bucket/o/"+url.PathEscape(name)+"?alt=media", nil)
	req.Host = "storage.googleapis.com"
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, "", 0
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.Header.Get("X-Goog-Generation"), resp.StatusCode
}
func (e *localGCS) reset(t *testing.T) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.endpoint+"/storage/v1/b/fixture-bucket/o", nil)
	req.Host = "storage.googleapis.com"
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct{ Items []struct{ Name string } }
	if json.NewDecoder(resp.Body).Decode(&list) != nil {
		t.Fatal("invalid emulator object list")
	}
	for _, o := range list.Items {
		req, _ := http.NewRequest("DELETE", e.endpoint+"/storage/v1/b/fixture-bucket/o/"+url.PathEscape(o.Name), nil)
		req.Host = "storage.googleapis.com"
		r, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != 200 && r.StatusCode != 204 {
			t.Fatal("emulator reset failed")
		}
	}
}
func emulatorFixture(t *testing.T) (Input, string, string) {
	i, tofu, cli := realFixture(t)
	text := `terraform {
 backend "gcs" {}
 required_providers { google = { source = "registry.opentofu.org/hashicorp/google", version = "7.10.0" } }
}
provider "google" { project = "fixture-project" }
resource "google_storage_bucket_object" "marker" {
 bucket = "fixture-bucket"
 name = "fixture/marker"
 content = "opentofu integration marker"
}
`
	write(t, filepath.Join(i.Worktree, "main.tf"), text)
	gitTest(t, i.Worktree, "add", ".")
	gitTest(t, i.Worktree, "commit", "-qm", "local GCS marker")
	i.Revision = gitTest(t, i.Worktree, "rev-parse", "HEAD")
	return i, tofu, cli
}

var fixtureGoogleHosts = []string{
	"storage.googleapis.com", "oauth2.googleapis.com", "iamcredentials.googleapis.com",
	"openidconnect.googleapis.com", "cloudbilling.googleapis.com", "cloudresourcemanager.googleapis.com",
}

func localGoogleHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(fixtureGoogleHosts, r.Host) {
			http.Error(w, "unexpected local test destination", 502)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func TestEmulatorNetworkBoundary(t *testing.T) {
	reached := false
	h := localGoogleHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	for _, host := range []string{"example.com", "metadata.google.internal", "storage.googleapis.com.attacker.invalid", "127.0.0.1:1"} {
		r := httptest.NewRequest("GET", "https://"+host+"/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 502 || reached {
			t.Fatal("unexpected destination reached transport")
		}
	}
}

func TestEmulatorConditionalLockCreation(t *testing.T) {
	if os.Getenv("C2OPS_TEST_NATIVE") != "1" {
		t.Skip("emulator qualification requires prepared executable")
	}
	f := &gcsFixture{objects: map[string][]byte{}, t: t}
	e := startEmulator(t, f)
	for _, want := range []int{200, 412} {
		r, _ := http.NewRequest("POST", e.endpoint+"/upload/storage/v1/b/fixture-bucket/o?uploadType=media&name=conditional.tflock&ifGenerationMatch=0", strings.NewReader("lock"))
		r.Host = "storage.googleapis.com"
		r.Header.Set("Content-Type", "application/json")
		response, err := e.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("conditional creation got %d want %d", response.StatusCode, want)
		}
	}
}
