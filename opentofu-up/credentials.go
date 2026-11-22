package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type credentials struct {
	env    []string
	expiry time.Time
}

func parseCredentials(data []byte, a Auth, dir string) (credentials, error) {
	var c credentials
	if a.Mode == "oauth_access_token" {
		var t struct {
			Token  string `json:"access_token"`
			Expiry string `json:"expires_at"`
		}
		if strict(data, &t) != nil || strings.TrimSpace(t.Token) == "" || strings.ContainsAny(t.Token, "\x00\r\n") {
			return c, errInvalid
		}
		expiry, e := time.Parse(time.RFC3339, t.Expiry)
		if e != nil {
			return c, errInvalid
		}
		_, off := expiry.Zone()
		if off != 0 {
			return c, errInvalid
		}
		c.expiry = expiry
		c.env = []string{"GOOGLE_OAUTH_ACCESS_TOKEN=" + t.Token}
		return c, nil
	}
	var k struct {
		Type       string `json:"type"`
		Project    string `json:"project_id"`
		KeyID      string `json:"private_key_id"`
		Key        string `json:"private_key"`
		Email      string `json:"client_email"`
		Client     string `json:"client_id"`
		TokenURI   string `json:"token_uri"`
		AuthURI    string `json:"auth_uri,omitempty"`
		Cert       string `json:"auth_provider_x509_cert_url,omitempty"`
		ClientCert string `json:"client_x509_cert_url,omitempty"`
		Universe   string `json:"universe_domain,omitempty"`
	}
	if strict(data, &k) != nil || k.Type != "service_account" || k.Email != a.Expected || !emailRE.MatchString(k.Email) || k.TokenURI != "https://oauth2.googleapis.com/token" || k.Project == "" || k.KeyID == "" || k.Client == "" || (k.Universe != "" && k.Universe != "googleapis.com") {
		return c, errInvalid
	}
	block, rest := pem.Decode([]byte(k.Key))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return c, errInvalid
	}
	var key *rsa.PrivateKey
	if block.Type == "PRIVATE KEY" {
		v, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return c, errInvalid
		}
		key, _ = v.(*rsa.PrivateKey)
	} else if block.Type == "RSA PRIVATE KEY" {
		key, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	if key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return c, errInvalid
	}
	p := filepath.Join(dir, "adc.json")
	if os.WriteFile(p, data, 0600) != nil {
		return c, errInvalid
	}
	c.env = []string{"GOOGLE_APPLICATION_CREDENTIALS=" + p}
	if a.Impersonate != "" {
		c.env = append(c.env, "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT="+a.Impersonate)
	}
	return c, nil
}
func (c credentials) validUntil(now time.Time, budget time.Duration) bool {
	return c.expiry.IsZero() || !c.expiry.Before(now.Add(budget+7*time.Minute))
}
