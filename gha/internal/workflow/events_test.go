package workflow

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTriggerFilters(t *testing.T) {
	for _, tc := range []struct {
		name, on, event, ref, action string
		paths                        []string
		want                         bool
		error                        string
	}{
		{name: "main", on: "push: {branches: [main]}", event: "push", ref: "refs/heads/main", want: true},
		{name: "feature", on: "push: {branches: [main]}", event: "push", ref: "refs/heads/topic"},
		{name: "tag excluded by branch filter", on: "push: {branches: [main]}", event: "push", ref: "refs/tags/main"},
		{name: "paths", on: "push: {paths: ['src/**', '!src/docs/**']}", event: "push", ref: "refs/heads/main", paths: []string{"src/main.go"}, want: true},
		{name: "negative paths", on: "push: {paths: ['src/**', '!src/docs/**']}", event: "push", ref: "refs/heads/main", paths: []string{"src/docs/README"}},
		{name: "missing paths", on: "push: {paths: ['src/**']}", event: "push", ref: "refs/heads/main", error: "changed_paths"},
		{name: "empty paths", on: "push: {paths: ['src/**']}", event: "push", ref: "refs/heads/main", paths: []string{}},
		{name: "tag ignores paths", on: "push: {tags: ['v*'], paths: ['src/**']}", event: "push", ref: "refs/tags/v1", want: true},
		{name: "pr base", on: "pull_request: {branches: [main]}", event: "pull_request", ref: "main", action: "synchronize", want: true},
		{name: "pr default types", on: "pull_request: {}", event: "pull_request", ref: "main", action: "closed"},
		{name: "pr explicit types", on: "pull_request: {types: [closed]}", event: "pull_request", ref: "main", action: "closed", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{EventName: tc.event, Event: map[string]any{"ref": tc.ref, "action": tc.action, "number": 12, "pull_request": map[string]any{"base": map[string]any{"ref": tc.ref}, "head": map[string]any{"ref": "feature"}}}, ChangedPaths: tc.paths}
			got, err := matchesTrigger([]byte("on:\n  "+tc.on+"\njobs: {}\n"), in)
			if tc.error != "" {
				require.ErrorContains(t, err, tc.error)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	matched, err := matchesTrigger([]byte("on: pull_request\njobs: {}"), Input{EventName: "pull_request", Event: map[string]any{"action": "closed"}})
	require.NoError(t, err)
	require.False(t, matched)
}

func TestRunnerProfilesRequireUnambiguousPlacement(t *testing.T) {
	profiles := []Profile{{Labels: []string{"linux", "arm64"}, ExecutionNeeds: map[string]any{"platform": "linux/arm64"}}, {Labels: []string{"linux", "amd64"}, ExecutionNeeds: map[string]any{"platform": "linux/amd64"}}}
	needs, err := matchProfile(profiles, []string{"linux", "ARM64"})
	require.NoError(t, err)
	require.Equal(t, "linux/arm64", needs["platform"])
	_, err = matchProfile(profiles, []string{"linux"})
	require.ErrorContains(t, err, "ambiguous")
	_, err = matchProfile(profiles, []string{"darwin"})
	require.ErrorContains(t, err, "no runner profile")
	require.ErrorContains(t, validateBindings(Input{Profiles: []Profile{{Labels: []string{"vm"}, ExecutionNeeds: map[string]any{"isolation": "vm"}}}}), "unsupported execution requirement")
}
