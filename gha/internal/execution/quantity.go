// Adapted from c2j v0.0.120 pkg/execution under Apache-2.0 (see /LICENSE).
// Only wire matching is retained; integration tests check runtime conformance.
package execution

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	cpuSyntax  = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(m?)$`)
	byteSyntax = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([kMGTPE]|[KMGTPE]i)$`)
)

func parseQuantity(value string, cpu bool) (resource.Quantity, int64, error) {
	invalid := func() (resource.Quantity, int64, error) {
		unit := "positive integral bytes with an explicit SI/IEC unit"
		if cpu {
			unit = "positive cores or millicores with at most millicore precision"
		}
		return resource.Quantity{}, 0, fmt.Errorf("%q must represent %s within int64 range", value, unit)
	}
	// Bound parsing work for untrusted metadata and payloads.
	if len(value) > 128 {
		return invalid()
	}
	syntax := byteSyntax
	if cpu {
		syntax = cpuSyntax
	}
	parts := syntax.FindStringSubmatch(value)
	if parts == nil {
		return invalid()
	}
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return invalid()
	}
	// ParseQuantity deliberately rounds sub-nano values and caps overflowing
	// BinarySI values. Check against an exact rational to reject either rather
	// than accepting a silently weakened requirement.
	exact, ok := new(big.Rat).SetString(parts[1])
	if !ok || exact.Sign() <= 0 {
		return invalid()
	}
	suffix := parts[2]
	multiplier := new(big.Int).SetInt64(1)
	if cpu {
		if suffix == "m" {
			exact.Quo(exact, big.NewRat(1000, 1))
		}
	} else {
		power := strings.Index("kMGTPE", strings.ReplaceAll(suffix[:1], "K", "k")) + 1
		base := int64(1000)
		if strings.HasSuffix(suffix, "i") {
			base = 1024
		}
		multiplier.Exp(big.NewInt(base), big.NewInt(int64(power)), nil)
		exact.Mul(exact, new(big.Rat).SetInt(multiplier))
	}
	parsed, ok := new(big.Rat).SetString(q.AsDec().String())
	if !ok || exact.Cmp(parsed) != 0 {
		return invalid()
	}
	if cpu {
		exact.Mul(exact, big.NewRat(1000, 1))
	}
	if !exact.IsInt() || !exact.Num().IsInt64() {
		return invalid()
	}
	return q, exact.Num().Int64(), nil
}
