package execution

import (
	"fmt"
	"regexp"
	"slices"
)

type Resources struct {
	CPU              *string `json:"cpu,omitempty"`
	Memory           *string `json:"memory,omitempty"`
	EphemeralStorage *string `json:"ephemeral-storage,omitempty"`
}
type Mismatch struct {
	Field     string `json:"field"`
	Required  string `json:"required"`
	Allocated string `json:"allocated,omitempty"`
	Reason    string `json:"reason"`
}

var capabilityName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._/-][a-z0-9]+)*$`)

func validateFacts(r Resources, caps []string) error {
	for _, cap := range caps {
		if !capabilityName.MatchString(cap) {
			return fmt.Errorf("invalid capability %q", cap)
		}
	}
	for _, f := range resourceFields(r, Resources{}) {
		if f.required != nil {
			if _, _, err := parseQuantity(*f.required, f.cpu); err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
		}
	}
	return nil
}

type field struct {
	name             string
	required, actual *string
	cpu              bool
}

func resourceFields(r, a Resources) []field {
	return []field{{"resources.cpu", r.CPU, a.CPU, true}, {"resources.memory", r.Memory, a.Memory, false}, {"resources.ephemeral-storage", r.EphemeralStorage, a.EphemeralStorage, false}}
}
func Compare(r Requirements, a Environment) ([]Mismatch, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	var out []Mismatch
	seen := map[string]bool{}
	for _, cap := range r.Capabilities {
		if !seen[cap] && !slices.Contains(a.Capabilities, cap) {
			out = append(out, Mismatch{Field: "capabilities", Required: cap, Reason: "missing_capability"})
		}
		seen[cap] = true
	}
	check := func(name, want, actual string, ok bool) {
		if !ok {
			reason := "incompatible"
			if actual == "" {
				reason = "unknown_allocation"
			}
			out = append(out, Mismatch{name, want, actual, reason})
		}
	}
	if r.Base != nil {
		check("base", *r.Base, a.Base, MatchBase(*r.Base, a.Base))
	}
	for _, f := range resourceFields(r.Resources, a.Resources) {
		if f.required == nil {
			continue
		}
		actual, compatible := "", false
		if f.actual != nil {
			actual = *f.actual
			_, want, _ := parseQuantity(*f.required, f.cpu)
			_, got, _ := parseQuantity(actual, f.cpu)
			compatible = got >= want
		}
		check(f.name, *f.required, actual, compatible)
	}
	return out, nil
}
