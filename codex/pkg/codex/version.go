package codex

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var codexVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// Compare numeric version components, never strings (0.99 is older than 0.148).
// Prereleases sort before the corresponding release; build metadata is ignored.
func validateCodexVersion(version string) error {
	match := codexVersionPattern.FindStringSubmatch(version)
	invalid := func() error {
		return fmt.Errorf("invalid Codex version %q; require codex-cli %s or later", version, minimumCodexVersion)
	}
	if match == nil {
		return invalid()
	}
	var components [3]uint64
	for i := range components {
		value, err := strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return invalid()
		}
		components[i] = value
	}
	for _, part := range strings.Split(match[4], ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return invalid()
		}
	}
	minimum := strings.Split(minimumCodexVersion, ".")
	for i, component := range components {
		min, _ := strconv.ParseUint(minimum[i], 10, 64)
		if component > min {
			return nil
		}
		if component < min {
			return fmt.Errorf("require codex-cli %s or later; got %s", minimumCodexVersion, version)
		}
	}
	if match[4] != "" {
		return fmt.Errorf("require codex-cli %s or later; got %s", minimumCodexVersion, version)
	}
	return nil
}
