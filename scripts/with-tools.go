//go:build ignore

// Run from the codex Go module so this test helper uses its c2j dependency.
// No production op imports this helper or c2j's setup implementation.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/colony-2/c2j/pkg/toolenv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	manifest := flag.String("manifest", "", "packaged op manifest containing declared dependencies")
	cwd := flag.String("cwd", "", "working directory for the test command")
	flag.Parse()
	data, err := os.ReadFile(*manifest)
	if err != nil {
		return err
	}
	var spec struct {
		Dependencies []string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return err
	}
	manager, err := toolenv.Default()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	environment, _, err := manager.Prepare(ctx, []toolenv.Scope{{ID: "op-test", Packages: spec.Dependencies}})
	if err != nil {
		return err
	}
	if !environment.Ready() {
		return fmt.Errorf("declared test tools are not ready")
	}
	if flag.NArg() == 0 {
		fmt.Println(environment.Path)
		return nil
	}
	if *cwd != "" {
		if err := os.Chdir(*cwd); err != nil {
			return err
		}
	}
	if err := os.Setenv("PATH", environment.Path+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return err
	}
	command, err := exec.LookPath(flag.Arg(0))
	if err != nil {
		return err
	}
	return syscall.Exec(command, flag.Args(), os.Environ())
}
