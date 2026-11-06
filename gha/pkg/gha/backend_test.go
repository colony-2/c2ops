package gha

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDefaultBackendRoutesDirectCallsToC2J(t *testing.T) {
	for _, backend := range []string{"", "  ", "c2j", " c2j "} {
		// C2j rejects synchronous timeout options before source/provisioner access.
		_, err := Run(context.Background(), RunInput{Backend: backend, Timeout: "1m"})
		require.ErrorContains(t, err, "backend c2j", backend)
	}
	for _, backend := range []string{"local", "github"} {
		b, err := backendFactory(backend)
		require.NoError(t, err)
		require.NotNil(t, b)
	}
	_, err := backendFactory("")
	require.Error(t, err, "the synchronous path must not silently default to local")
}
