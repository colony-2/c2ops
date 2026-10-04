package extensioncmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"

	"github.com/colony-2/c2ops/codex/pkg/checkpoint"
)

type ExternalRef struct {
	URL    string `json:"url"`
	Expand bool   `json:"expand,omitempty"`
}

type ArtifactRef struct {
	Kind     string       `json:"kind"`
	Name     string       `json:"name,omitempty"`
	External *ExternalRef `json:"external,omitempty"`
}

type Result[O any] struct {
	Output       O
	ArtifactRefs map[string]ArtifactRef
	Objects      map[string]checkpoint.Draft
}

type envelope[O any] struct {
	Output       O                           `json:"output,omitempty"`
	ArtifactRefs map[string]ArtifactRef      `json:"artifact_refs,omitempty"`
	Objects      map[string]checkpoint.Draft `json:"objects,omitempty"`
}

func NewExternalArtifactRef(name string, url string, expand bool) ArtifactRef {
	return ArtifactRef{
		Kind: "external",
		Name: name,
		External: &ExternalRef{
			URL:    url,
			Expand: expand,
		},
	}
}

func Main[I any, O any](run func(context.Context, I) (Result[O], error)) {
	os.Exit(Run(run))
}

func Run[I any, O any](run func(context.Context, I) (Result[O], error)) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, _ = fmt.Fprintln(os.Stderr, "codex-op: waiting for JSON input on stdin (30s timeout)")
	input, err := readInput[I](ctx, os.Stdin, 30*time.Second)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "decode input: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintln(os.Stderr, "codex-op: input decoded; starting invocation")
	result, err := run(ctx, input)
	env := envelope[O]{
		Output:       result.Output,
		ArtifactRefs: result.ArtifactRefs,
		Objects:      result.Objects,
	}
	if err != nil {
		env.Objects = nil
		if hasEnvelopeData(env) {
			if encodeErr := json.NewEncoder(os.Stdout).Encode(env); encodeErr != nil {
				_, _ = fmt.Fprintf(os.Stderr, "encode output: %v\n", encodeErr)
				return 1
			}
		}
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}

	if err := json.NewEncoder(os.Stdout).Encode(env); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "encode output: %v\n", err)
		return 1
	}

	return 0
}

func decodeInput[I any](r io.Reader) (I, error) {
	var input I
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return input, err
	}
	// The protocol is one JSON value. Waiting for a second Decode requires EOF,
	// which interactive/container stdin transports may never send.
	return input, nil
}

func readInput[I any](ctx context.Context, stdin io.ReadCloser, timeout time.Duration) (I, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type decoded struct {
		input I
		err   error
	}
	done := make(chan decoded, 1)
	go func() { input, err := decodeInput[I](stdin); done <- decoded{input, err} }()
	select {
	case result := <-done:
		return result.input, result.err
	case <-ctx.Done():
		_ = stdin.Close()
		var zero I
		return zero, fmt.Errorf("waiting for extension JSON input: %w; check c2j stdin forwarding (especially sandbox.type=shai)", ctx.Err())
	}
}

func hasEnvelopeData[O any](env envelope[O]) bool {
	if len(env.ArtifactRefs) > 0 {
		return true
	}
	value := reflect.ValueOf(env.Output)
	if !value.IsValid() {
		return false
	}
	return !value.IsZero()
}
