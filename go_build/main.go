// go.build compiles a verified Git projection using an immutable Nix SDK.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unicode/utf8"
)

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "go.build: SDK source builds are forbidden")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "go.build:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, in io.Reader, out io.Writer) error {
	// Do not include supplied values or subprocess output in errors: either may
	// contain private proxy URLs, helpers or tokens.
	data, err := io.ReadAll(io.LimitReader(in, 128*1024+1))
	if err != nil || len(data) > 128*1024 || !utf8.Valid(data) {
		return fmt.Errorf("invalid input size")
	}
	var req Request
	if err := strictJSON(data, &req); err != nil {
		return fmt.Errorf("invalid input JSON or unknown field")
	}
	if err := req.validate(); err != nil {
		return err
	}
	output, err := build(ctx, req)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"output": output})
}

func strictJSON(data []byte, dst any) error {
	// encoding/json accepts duplicate keys. Reject them before decoding identities.
	dec := json.NewDecoder(bytesReader(data))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' {
				seen := map[string]bool{}
				for dec.More() {
					key, err := dec.Token()
					if err != nil {
						return err
					}
					s, ok := key.(string)
					if !ok || seen[s] {
						return fmt.Errorf("duplicate key")
					}
					seen[s] = true
					if err := walk(); err != nil {
						return err
					}
				}
			} else if d == '[' {
				for dec.More() {
					if err := walk(); err != nil {
						return err
					}
				}
			} else {
				return fmt.Errorf("invalid JSON")
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	dec = json.NewDecoder(bytesReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
