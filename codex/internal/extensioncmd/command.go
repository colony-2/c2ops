package extensioncmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"

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
	input, err := decodeInput[I](os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "decode input: %v\n", err)
		return 1
	}

	result, err := run(context.Background(), input)
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
	if err := dec.Decode(&input); err != nil && err != io.EOF {
		return input, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return input, fmt.Errorf("expected one JSON input object")
	}
	return input, nil
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
