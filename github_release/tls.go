package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
)

// Go's macOS verifier uses the keychain and ignores SSL_CERT_FILE. Honor an
// explicit bundle for this client only; other hosts retain Go's trust loading.
func githubTransport(goos, certFile string) (http.RoundTripper, error) {
	if goos != "darwin" || certFile == "" {
		return http.DefaultTransport, nil
	}
	configurationError := errors.New("SSL_CERT_FILE must contain a readable PEM certificate bundle")
	bundle, err := os.ReadFile(certFile)
	if err != nil {
		return nil, configurationError
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		return nil, configurationError
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("cannot configure GitHub TLS transport")
	}
	transport := base.Clone() // Also clones a nonnil TLSClientConfig.
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	} else {
		transport.TLSClientConfig.RootCAs = roots
	}
	return transport, nil
}
