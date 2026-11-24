// Package pty selects the terminal transport for act host jobs.
package pty

import (
	"errors"
	"os"
	"runtime"

	upstream "github.com/colony-2/c2ops/gha/upstream/pty"
)

func Open() (*os.File, *os.File, error) {
	// act uses its noninteractive pipe transport when a PTY is unavailable.
	// On Darwin its PTY can hang even for the initial Node executable probe.
	if runtime.GOOS == "darwin" {
		return nil, nil, errors.New("use pipes for Darwin host jobs")
	}
	return upstream.Open()
}
