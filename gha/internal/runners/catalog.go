// Package runners maps explicit runner labels to our versioned executor inventory.
package runners

import (
	"fmt"
	"slices"
	"strings"

	"github.com/colony-2/c2ops/gha/internal/execution"
)

const Version = "2026-11-06.1"
const LinuxAMD = "ubuntu-26.04-amd64"
const LinuxARM = "ubuntu-26.04-arm64"
const MacARM = "macos-sequoia-15.7.7-arm64"

var bases = []string{LinuxAMD, LinuxARM, MacARM}

type Type struct {
	Image        string `json:"image"`
	Architecture string `json:"architecture"`
	Size         string `json:"size"`
}
type Profile struct {
	Labels         []string                `json:"labels"`
	ExecutionNeeds map[string]any          `json:"execution_needs,omitempty"`
	Required       *execution.Requirements `json:"execution_required,omitempty"`
	Type           *Type                   `json:"runner_type,omitempty"`
}
type Resolution struct {
	Version  string                 `json:"catalog_version"`
	Source   string                 `json:"source"`
	Labels   []string               `json:"labels"`
	Required execution.Requirements `json:"execution_required"`
}
type entry struct{ base, cpu, memory, reason string }

var catalog = buildCatalog()

func buildCatalog() map[string]entry {
	m := map[string]entry{}
	for _, image := range []string{"24.04", "26.04", "latest"} {
		for _, arch := range []string{"amd64", "arm64"} {
			label := "ubuntu-" + image
			base := LinuxAMD
			if arch == "arm64" {
				label += "-arm"
				base = LinuxARM
			}
			reason := "catalog image " + label + " mapped to " + base
			// ubuntu-latest-arm is a local convenience alias, explicitly versioned.
			m[label] = entry{base, "4", "16Gi", reason}
			for i, cpu := range []string{"2", "4", "8", "16", "32", "64", "96"} {
				if arch == "arm64" && cpu == "96" {
					continue
				}
				memory := []string{"8G", "16G", "32G", "64G", "128G", "256G", "384G"}[i]
				if arch == "arm64" && cpu == "64" {
					memory = "208G"
				}
				m[label+"-"+cpu+"core"] = entry{base, cpu, memory, reason + "; size " + cpu + "core"}
			}
		}
	}
	for _, image := range []string{"14", "15"} {
		m["macos-"+image] = entry{MacARM, "3", "8Gi", "catalog macos-" + image + " mapped to " + MacARM}
		m["macos-"+image+"-xlarge"] = entry{MacARM, "5", "14G", "catalog macos-" + image + "-xlarge mapped to " + MacARM}
	}
	m["macos-latest-xlarge"] = m["macos-15-xlarge"]
	return m
}
func (e entry) requirements() execution.Requirements {
	return execution.Requirements{Base: execution.String(e.base), Resources: execution.Resources{CPU: execution.String(e.cpu), Memory: execution.String(e.memory)}}
}
func (t Type) Resolve() (execution.Requirements, error) {
	label := t.Image
	if strings.HasPrefix(label, "ubuntu-") {
		if !slices.Contains([]string{"ubuntu-24.04", "ubuntu-26.04", "ubuntu-latest"}, label) {
			return execution.Requirements{}, fmt.Errorf("unsupported runner image %q", label)
		}
		switch t.Architecture {
		case "amd64":
		case "arm64":
			label += "-arm"
		default:
			return execution.Requirements{}, fmt.Errorf("unsupported architecture %q", t.Architecture)
		}
		label += "-" + t.Size
	} else if slices.Contains([]string{"macos-14", "macos-15"}, label) && t.Architecture == "arm64" && t.Size == "xlarge" {
		label += "-xlarge"
	} else {
		return execution.Requirements{}, fmt.Errorf("unsupported runner type %+v", t)
	}
	e, ok := catalog[label]
	if !ok {
		return execution.Requirements{}, fmt.Errorf("unsupported runner type %+v", t)
	}
	return e.requirements(), nil
}
func ValidateProfiles(profiles []Profile) error {
	for _, p := range profiles {
		if len(p.Labels) == 0 {
			return fmt.Errorf("runner profile requires labels")
		}
		for _, label := range p.Labels {
			if strings.TrimSpace(label) == "" {
				return fmt.Errorf("empty runner label")
			}
		}
		if p.ExecutionNeeds != nil {
			return fmt.Errorf("negotiated profiles cannot contain execution_needs")
		}
		if (p.Required == nil) == (p.Type == nil) {
			return fmt.Errorf("runner profile requires exactly one of execution_required or runner_type")
		}
		r, err := p.requirements()
		if err != nil {
			return err
		}
		if err = ValidatePlacement(p.Labels, r); err != nil {
			return err
		}
	}
	return nil
}
func (p Profile) requirements() (execution.Requirements, error) {
	if p.Type != nil {
		return p.Type.Resolve()
	}
	if p.Required == nil {
		return execution.Requirements{}, fmt.Errorf("missing requirements")
	}
	return *p.Required, p.Required.Validate()
}

// Known OS/architecture labels may not be overridden by an incompatible base.
func ValidatePlacement(labels []string, r execution.Requirements) error {
	if err := r.Validate(); err != nil {
		return err
	}
	for _, raw := range labels {
		label := strings.ToLower(raw)
		os, arch := "", ""
		if strings.HasPrefix(label, "windows") {
			return fmt.Errorf("unsupported Windows runner %q", raw)
		}
		if strings.HasPrefix(label, "macos-") || strings.HasPrefix(label, "xcode-") {
			os = "macos"
			arch = "arm64"
			if strings.Contains(label, "intel") || strings.HasSuffix(label, "-large") || slices.Contains([]string{"macos-10.15", "macos-11", "macos-12", "macos-13"}, label) {
				return fmt.Errorf("unsupported Intel macOS runner %q", raw)
			}
		} else if strings.HasPrefix(label, "ubuntu-") {
			os = "ubuntu"
			arch = "amd64"
			if strings.Contains(label, "-arm") {
				arch = "arm64"
			}
		}
		if os == "" {
			continue
		}
		if r.Base == nil {
			return fmt.Errorf("runner %q requires an explicit compatible base", raw)
		}
		matched := false
		for _, base := range bases {
			if execution.MatchBase(*r.Base, base) {
				matched = true
				if !strings.HasPrefix(base, os+"-") || !strings.HasSuffix(base, "-"+arch) {
					return fmt.Errorf("base %q conflicts with runner %q", *r.Base, raw)
				}
			}
		}
		if !matched {
			return fmt.Errorf("base %q is outside supported inventory %v", *r.Base, bases)
		}
	}
	return nil
}
func Resolve(labels []string, profiles []Profile) (Resolution, error) {
	out := Resolution{Version: Version, Labels: append([]string(nil), labels...)}
	if len(labels) == 0 {
		return out, fmt.Errorf("job requires runs-on")
	}
	if err := ValidateProfiles(profiles); err != nil {
		return out, err
	}
	var found *Profile
	for i, p := range profiles {
		matches := true
		for _, label := range labels {
			matches = matches && slices.ContainsFunc(p.Labels, func(v string) bool { return strings.EqualFold(v, label) })
		}
		if matches {
			if found != nil {
				return out, fmt.Errorf("ambiguous runner profile for %v", labels)
			}
			found = &profiles[i]
		}
	}
	if found != nil {
		r, err := found.requirements()
		if err != nil {
			return out, err
		}
		if err = ValidatePlacement(labels, r); err != nil {
			return out, err
		}
		out.Required = r
		out.Source = "explicit runner profile"
		return out, nil
	}
	if len(labels) == 1 {
		if e, ok := catalog[strings.ToLower(labels[0])]; ok {
			out.Required = e.requirements()
			out.Source = e.reason
			return out, nil
		}
	}
	return out, fmt.Errorf("unsupported runner %v: no catalog mapping or explicit profile; available bases: %v", labels, bases)
}
