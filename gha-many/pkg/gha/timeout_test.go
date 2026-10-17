package gha

import (
	"testing"
	"time"
)

func TestTimeoutIsOptIn(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", 0}, {"  ", 0}, {"0", 0}, {"45m", 45 * time.Minute},
	} {
		got, err := parseTimeout(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("parseTimeout(%q) = %v, %v; want %v", tc.raw, got, err, tc.want)
		}
	}
	if _, err := parseTimeout("invalid"); err == nil {
		t.Fatal("invalid explicit timeout must fail")
	}
}
