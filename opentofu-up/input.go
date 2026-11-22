package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"

	"filippo.io/age"
)

const maxInput = 1 << 20

var errInvalid = errors.New("invalid request")
var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,255}$`)
var variableRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
var revisionRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var durationRE = regexp.MustCompile(`^[1-9][0-9]*[sm]$`)
var emailRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+@[a-zA-Z0-9.-]+\.gserviceaccount\.com$`)
var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)

type Backend struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
}
type Auth struct {
	Mode        string `json:"mode"`
	SecretName  string `json:"secret_name"`
	Expected    string `json:"expected_service_account,omitempty"`
	Impersonate string `json:"impersonate_service_account,omitempty"`
}
type Recovery struct {
	Recipients []string `json:"age_recipients"`
}
type Input struct {
	Worktree    string         `json:"worktree_path"`
	Revision    string         `json:"revision"`
	Root        string         `json:"root"`
	Mode        string         `json:"mode"`
	Backend     Backend        `json:"backend"`
	Auth        Auth           `json:"google_auth"`
	Variables   map[string]any `json:"variables,omitempty"`
	Timeout     string         `json:"timeout,omitempty"`
	LockTimeout string         `json:"lock_timeout,omitempty"`
	Recovery    *Recovery      `json:"recovery,omitempty"`
	total, lock time.Duration
	recipients  []age.Recipient
}

// Public variables may contain null; all fixed-shape fields reject null.
func uniqueAllowNull(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return errInvalid
	}
	if x, ok := t.(json.Delim); ok {
		if x == '{' {
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return errInvalid
				}
				s := k.(string)
				if seen[s] {
					return errInvalid
				}
				seen[s] = true
				if e = uniqueAllowNull(d); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		if x == '[' {
			for d.More() {
				if e = uniqueAllowNull(d); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		return errInvalid
	}
	return nil
}

// encoding/json matches struct fields case-insensitively; the manifest does
// not. Check exact keys and nullability before decoding the public contract.
func exactShape(data []byte, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Interface {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errInvalid
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return errInvalid
		}
		allowed := map[string]reflect.Type{}
		for j := 0; j < t.NumField(); j++ {
			f := t.Field(j)
			if f.PkgPath != "" {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = f.Type
			}
		}
		for k, v := range fields {
			ft, ok := allowed[k]
			if !ok || exactShape(v, ft) != nil {
				return errInvalid
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return errInvalid
		}
		for _, v := range values {
			if exactShape(v, t.Elem()) != nil {
				return errInvalid
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return errInvalid
		}
		for _, v := range values {
			if exactShape(v, t.Elem()) != nil {
				return errInvalid
			}
		}
	}
	return nil
}
func strict(data []byte, target any) error {
	if len(data) > maxInput || exactShape(data, reflect.TypeOf(target)) != nil {
		return errInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if e := uniqueAllowNull(d); e != nil {
		return errInvalid
	}
	if _, e := d.Token(); e != io.EOF {
		return errInvalid
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if e := d.Decode(target); e != nil {
		return errInvalid
	}
	return nil
}
func canonical(s string, dot bool) bool {
	if dot && s == "." {
		return true
	}
	if s == "" || path.IsAbs(s) || path.Clean(s) != s || strings.Contains(s, "\\") {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	for _, p := range strings.Split(s, "/") {
		if p == "." || p == ".." || p == "" {
			return false
		}
	}
	return true
}
func parseInput(r io.Reader) (Input, error) {
	var i Input
	b, e := io.ReadAll(io.LimitReader(r, maxInput+1))
	if e != nil || strict(b, &i) != nil {
		return i, errInvalid
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if i.Worktree == "" || !revisionRE.MatchString(i.Revision) || !canonical(i.Root, true) || (i.Mode != "plan" && i.Mode != "up") || !bucketRE.MatchString(i.Backend.Bucket) || !canonical(i.Backend.Prefix, false) || !nameRE.MatchString(i.Auth.SecretName) {
		return i, errInvalid
	}
	var authFields map[string]json.RawMessage
	_ = json.Unmarshal(raw["google_auth"], &authFields)
	if i.Auth.Mode == "oauth_access_token" {
		if i.Auth.Expected != "" || i.Auth.Impersonate != "" {
			return i, errInvalid
		}
		for _, k := range []string{"expected_service_account", "impersonate_service_account"} {
			if _, present := authFields[k]; present {
				return i, errInvalid
			}
		}
	} else if i.Auth.Mode == "service_account_json" {
		if _, present := authFields["impersonate_service_account"]; present && !emailRE.MatchString(i.Auth.Impersonate) {
			return i, errInvalid
		}
		if !emailRE.MatchString(i.Auth.Expected) || (i.Auth.Impersonate != "" && !emailRE.MatchString(i.Auth.Impersonate)) {
			return i, errInvalid
		}
	} else {
		return i, errInvalid
	}
	if v, ok := raw["timeout"]; ok && string(v) == `""` {
		return i, errInvalid
	}
	if v, ok := raw["lock_timeout"]; ok && string(v) == `""` {
		return i, errInvalid
	}
	if i.Timeout == "" {
		i.Timeout = "30m"
	}
	if i.LockTimeout == "" {
		i.LockTimeout = "2m"
	}
	if !durationRE.MatchString(i.Timeout) || !durationRE.MatchString(i.LockTimeout) {
		return i, errInvalid
	}
	i.total, e = time.ParseDuration(i.Timeout)
	if e != nil || i.total > time.Hour {
		return i, errInvalid
	}
	i.lock, e = time.ParseDuration(i.LockTimeout)
	if e != nil || i.lock > i.total {
		return i, errInvalid
	}
	for k := range i.Variables {
		if !variableRE.MatchString(k) {
			return i, errInvalid
		}
	}
	if i.Mode == "up" && i.Recovery == nil {
		return i, errInvalid
	}
	if i.Recovery != nil {
		seen := map[string]bool{}
		if len(i.Recovery.Recipients) == 0 {
			return i, errInvalid
		}
		for _, s := range i.Recovery.Recipients {
			if seen[s] {
				return i, errInvalid
			}
			seen[s] = true
			r, e := age.ParseX25519Recipient(s)
			if e != nil {
				return i, errInvalid
			}
			i.recipients = append(i.recipients, r)
		}
	}
	return i, nil
}
