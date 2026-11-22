package main

import (
	"bufio"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type fixtureErrors []string

func (e *fixtureErrors) Error(args ...any) { *e = append(*e, fmt.Sprint(args...)) }
func (e *fixtureErrors) Errorf(format string, args ...any) {
	*e = append(*e, fmt.Sprintf(format, args...))
}

func TestFixtureImpersonationRefusals(t *testing.T) {
	for _, kind := range []string{"valid", "malformed", "missing-scope", "delegates", "wrong-source", "wrong-target"} {
		t.Run(kind, func(t *testing.T) {
			body := `{"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"3600s"}`
			path := "/v1/projects/-/serviceAccounts/target@fixture.iam.gserviceaccount.com:generateAccessToken"
			token := "fixture-source"
			switch kind {
			case "malformed":
				body = `{`
			case "missing-scope":
				body = `{"lifetime":"3600s"}`
			case "delegates":
				body = `{"scope":["https://www.googleapis.com/auth/cloud-platform"],"delegates":["unexpected"]}`
			case "wrong-source":
				token = "wrong-source"
			case "wrong-target":
				path = "/v1/projects/-/serviceAccounts/wrong:generateAccessToken"
			}
			var failures fixtureErrors
			fixture := &gcsFixture{auth: "fixture-source", t: &failures}
			request := httptest.NewRequest(http.MethodPost, "https://iamcredentials.googleapis.com"+path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			fixture.ServeHTTP(response, request)
			if kind == "valid" {
				if response.Code != http.StatusOK || len(failures) != 0 || len(fixture.tokens) != 1 {
					t.Fatalf("valid request: status=%d failures=%v tokens=%d", response.Code, failures, len(fixture.tokens))
				}
			} else if response.Code != http.StatusBadRequest || len(failures) != 1 || len(fixture.tokens) != 0 {
				t.Fatalf("refusal: status=%d failures=%v tokens=%d", response.Code, failures, len(fixture.tokens))
			}
		})
	}
}

// A provider can exit with an asynchronous refresh still writing its request.
// Incomplete HTTP messages must not reach the credential-claims oracle.
func TestGoogleProxyRejectsTruncatedRequests(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			dispatched := make(chan bool, 1)
			proxy, ca := googleProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				dispatched <- true
				w.WriteHeader(http.StatusNoContent)
			}))
			address, err := url.Parse(proxy)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := net.DialTimeout("tcp", address.Host, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, err = io.WriteString(conn, "CONNECT iamcredentials.googleapis.com:443 HTTP/1.1\r\nHost: iamcredentials.googleapis.com:443\r\n\r\n")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"}); err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			pem, err := os.ReadFile(ca)
			if err != nil || !roots.AppendCertsFromPEM(pem) {
				t.Fatalf("fixture CA: %v", err)
			}
			tc := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: "iamcredentials.googleapis.com", MinVersion: tls.VersionTLS12})
			body := `{"scope":["https://www.googleapis.com/auth/cloud-platform"]}`
			length := len(body)
			if !complete {
				body = body[:length/2]
			}
			_, err = fmt.Fprintf(tc, "POST /v1/projects/-/serviceAccounts/target@fixture.iam.gserviceaccount.com:generateAccessToken HTTP/1.1\r\nHost: iamcredentials.googleapis.com\r\nContent-Length: %d\r\n\r\n%s", length, body)
			if err != nil {
				t.Fatal(err)
			}
			if err = tc.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(tc), &http.Request{Method: "POST"})
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			want := http.StatusBadRequest
			if complete {
				want = http.StatusNoContent
			}
			if response.StatusCode != want {
				t.Fatalf("status=%d, want %d", response.StatusCode, want)
			}
			select {
			case <-dispatched:
				if !complete {
					t.Fatal("truncated request reached claims oracle")
				}
			default:
				if complete {
					t.Fatal("complete request did not reach claims oracle")
				}
			}
		})
	}
}

// Verify the fixture's auth oracle rejects signed-but-invalid claims as well as
// forged assertions. Otherwise an accepting fixture could hide SDK auth defects.
func TestFixtureJWTVerification(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	other, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().Truncate(time.Second)
	for _, kind := range []string{"valid", "signature", "issuer", "subject", "audience", "expired", "future", "lifetime", "scope", "algorithm"} {
		t.Run(kind, func(t *testing.T) {
			claims := map[string]any{"iss": "fixture@fixture.iam.gserviceaccount.com", "aud": "https://oauth2.googleapis.com/token", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "scope": "https://www.googleapis.com/auth/cloud-platform"}
			header := map[string]string{"alg": "RS256", "typ": "JWT"}
			signer := key
			switch kind {
			case "signature":
				signer = other
			case "issuer":
				claims["iss"] = "attacker@fixture.iam.gserviceaccount.com"
			case "subject":
				claims["sub"] = "other@fixture.iam.gserviceaccount.com"
			case "audience":
				claims["aud"] = "https://other.invalid/token"
			case "expired":
				claims["exp"] = now.Unix()
			case "future":
				claims["iat"] = now.Add(time.Hour).Unix()
			case "lifetime":
				claims["exp"] = now.Add(2 * time.Hour).Unix()
			case "scope":
				claims["scope"] = ""
			case "algorithm":
				header["alg"] = "none"
			}
			h, _ := json.Marshal(header)
			b, _ := json.Marshal(claims)
			unsigned := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(b)
			digest := sha256.Sum256([]byte(unsigned))
			sig, e := rsa.SignPKCS1v15(rand.Reader, signer, crypto.SHA256, digest[:])
			if e != nil {
				t.Fatal(e)
			}
			err := verifyAssertion(unsigned+"."+base64.RawURLEncoding.EncodeToString(sig), &key.PublicKey, now)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("JWT verification %s: %v", kind, err)
			}
		})
	}
	if verifyAssertion(strings.Repeat("x", 30), &key.PublicKey, now) == nil {
		t.Fatal("accepted malformed assertion")
	}
}
