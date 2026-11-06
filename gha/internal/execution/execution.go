// Package execution implements the extension-facing subset of c2j's contract.
package execution

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Requirements deliberately cannot serialize packages or legacy placement fields.
type Requirements struct {
	Base         *string   `json:"base,omitempty"`
	Resources    Resources `json:"resources,omitzero"`
	Capabilities []string  `json:"capabilities,omitempty"`
}

func (r Requirements) Validate() error {
	if r.Base == nil && r.Resources == (Resources{}) && len(r.Capabilities) == 0 {
		return fmt.Errorf("execution_required must be nonempty")
	}
	if r.Base != nil {
		if err := validateBase(*r.Base, false); err != nil {
			return err
		}
	}
	return validateFacts(r.Resources, r.Capabilities)
}

// Reject nulls and duplicate keys before decoding: neither may erase constraints.
func strictJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var visit func() error
	visit = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if t == nil {
			return fmt.Errorf("null is not a requirement")
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return fmt.Errorf("duplicate requirement field")
					}
					seen[name] = true
					if err := visit(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := visit(); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("invalid requirement JSON")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
func (r *Requirements) UnmarshalJSON(raw []byte) error {
	if err := strictJSON(raw); err != nil {
		return err
	}
	type wire Requirements
	var v wire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	*r = Requirements(v)
	return r.Validate()
}

// Environment is attempt-local runtime context, never durable task input.
type Environment struct {
	SchemaVersion int       `json:"schema_version"`
	Base          string    `json:"base,omitempty"`
	Resources     Resources `json:"resources,omitempty"`
	Capabilities  []string  `json:"capabilities,omitempty"`
	Packages      []string  `json:"packages"`
}

func ReadEnvironment() (Environment, error) {
	raw, ok := os.LookupEnv("C2J_EXECUTION_ALLOCATION")
	if !ok {
		return Environment{}, fmt.Errorf("negotiation requires C2J_EXECUTION_ALLOCATION from c2j")
	}
	var env Environment
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return env, fmt.Errorf("allocation: %w", err)
	}
	return env, env.Validate()
}
func (e Environment) Validate() error {
	if e.SchemaVersion != 2 {
		return fmt.Errorf("negotiation requires allocation schema 2")
	}
	if e.Base != "" {
		if err := validateBase(e.Base, true); err != nil {
			return err
		}
	}
	return validateFacts(e.Resources, e.Capabilities)
}
func String(s string) *string { return &s }
