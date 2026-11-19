package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type sdkPin struct {
	revision  string
	attribute string
	paths     map[string]string
}

// Evaluated with allow-import-from-derivation=false. These are SDK outputs,
// not source tarballs or moving aliases. Patch changes require an op revision.
var sdkPins = map[string]sdkPin{
	"1.26.1": {"e4074f13905f5117d20ad71358596b41694863aa", "go_1_26", map[string]string{
		"aarch64-linux":  "wjr8n27223n15jb0:5yibi4z4ilkkgnp0-go-1.26.1",
		"x86_64-linux":   "p0dvaja31p74hspq:217b2zq0bcrn69gb-go-1.26.1",
		"aarch64-darwin": "jh7awgszjdnhi8cq:zqf46v2zm8gdsrq9-go-1.26.1"}},
	"1.26.8": {"e7439b6b14ad3cc35d05608ebca9bce01a25f5f8", "go_1_26", map[string]string{
		"aarch64-linux":  "3677bfwiaibp0fs0:dxijmn8bsmkx31vx-go-1.26.8",
		"x86_64-linux":   "iqq9yz2yrcrzsn52:h3vkkz72i92h1zds-go-1.26.8",
		"aarch64-darwin": "isqxa82c7f65sbhi:51npr0547yz87bh5-go-1.26.8"}},
}

func hostSystem() string {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	return arch + "-" + runtime.GOOS
}
func sdkInstallArgs(store, link, hook string, prepared bool) []string {
	args := []string{"--extra-experimental-features", "nix-command flakes", "build", "--no-write-lock-file", "--out-link", link, "--max-jobs", "0", "--builders", "", "--option", "allow-import-from-derivation", "false", "--option", "fallback", "false", "--option", "require-sigs", "true", "--option", "pre-build-hook", hook}
	if prepared {
		args = append(args, "--offline")
	}
	return append(args, "--", store)
}
func acquireSDK(ctx context.Context, s selection, scratch string, run runner) (SDKInfo, error) {
	pin := sdkPins[s.resolved]
	host := hostSystem()
	storeID := pin.paths[host]
	if storeID == "" {
		return SDKInfo{}, fmt.Errorf("unsupported SDK package host")
	}
	store := sdkStorePath(storeID)
	compiler := filepath.Join(store, "bin/go")
	info := SDKInfo{s.requested, &s.file, s.directive, s.resolved, "", pin.revision, pin.attribute, store, compiler, host}
	env := cleanEnv()
	_, valid := run(ctx, "nix", []string{"--extra-experimental-features", "nix-command", "path-info", "--offline", store}, "", env)
	prepared := valid == nil
	// A Go executable exits nonzero on empty JSON and on all non-JSON arguments.
	// It therefore doubles as the rejecting pre-build hook, without a shell or a
	// mutable script. Nix cannot run any builder, including preferLocalBuild.
	hook, err := os.Executable()
	if err != nil {
		return info, fmt.Errorf("cannot locate SDK build rejection hook")
	}
	hook, err = filepath.EvalSymlinks(hook)
	if err != nil {
		return info, fmt.Errorf("cannot resolve SDK build rejection hook")
	}
	if _, err = run(ctx, "nix", sdkInstallArgs(store, filepath.Join(scratch, "sdk-root"), hook, prepared), "", env); err != nil {
		return info, fmt.Errorf("Go %s pinned prebuilt SDK unavailable (%s, %s); source builds and IFD are disabled", s.resolved, pin.revision, host)
	}
	real, err := filepath.EvalSymlinks(compiler)
	if err != nil || !inside(store, real) {
		return info, fmt.Errorf("mapped absolute Go compiler is missing or escapes SDK")
	}
	observed, err := run(ctx, compiler, []string{"version"}, "", envWith(env, map[string]string{"GOENV": "off", "GOTOOLCHAIN": "local", "GOROOT": store + "/share/go", "GOWORK": "off"}))
	if err != nil {
		return info, fmt.Errorf("Go %s compiler version check failed", s.resolved)
	}
	want := "go version go" + s.resolved + " " + runtime.GOOS + "/" + runtime.GOARCH
	if strings.TrimSpace(string(observed)) != want {
		return info, fmt.Errorf("Go SDK mismatch: requested %s; observed %s", s.resolved, safeObserved(observed))
	}
	info.ActualVersion = s.resolved
	return info, nil
}
func safeObserved(b []byte) string {
	fields := strings.Fields(string(b))
	if len(fields) == 4 && fields[0] == "go" && fields[1] == "version" {
		if _, err := parseVersion(fields[2]); err == nil {
			return strings.TrimPrefix(fields[2], "go")
		}
	}
	return "invalid version response"
}

// Split store hashes avoid retaining every SDK as a runtime closure reference.
func sdkStorePath(id string) string { return "/nix/store/" + strings.ReplaceAll(id, ":", "") }
