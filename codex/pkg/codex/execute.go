package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var codexCredentialFileNames = []string{
	"auth.json",
	"config.toml",
}

// Execute runs Codex in non-interactive mode and returns the normalized result.
// The caller is responsible for managing the returned stdoutPath, stderrPath, and artifactDir.
func Execute(ctx context.Context, opts Options) (Result, string, string, string, error) {
	if err := opts.validate(); err != nil {
		return Result{}, "", "", "", err
	}
	if err := ensureExecutionPaths(opts); err != nil {
		return Result{}, "", "", "", err
	}
	activeOpts := opts
	activeCodexHome, err := resolveDirectCodexHome(opts)
	if err != nil {
		return Result{}, "", "", "", err
	}
	activeOpts.CodexHome = activeCodexHome
	if err := os.MkdirAll(activeOpts.CodexHome, 0o755); err != nil {
		return Result{}, "", "", "", fmt.Errorf("create active codex home path: %w", err)
	}

	artifactDir, err := os.MkdirTemp("", "codex-artifacts-*")
	if err != nil {
		return Result{}, "", "", "", fmt.Errorf("create artifacts dir: %w", err)
	}
	schemaPath, schemaCleanup, err := writeSchemaTempFile(activeOpts.StructuredSchema)
	if err != nil {
		_ = os.RemoveAll(artifactDir)
		return Result{}, "", "", "", err
	}
	defer schemaCleanup()

	stdoutPath := opts.stdoutPath(artifactDir)
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		_ = os.RemoveAll(artifactDir)
		return Result{}, "", "", "", fmt.Errorf("create stdout capture: %w", err)
	}
	debugArtifactStat("stdout-create", stdoutPath)

	stderrPath := opts.stderrPath(artifactDir)
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		stdoutFile.Close()
		_ = os.RemoveAll(artifactDir)
		return Result{}, "", "", "", fmt.Errorf("create stderr capture: %w", err)
	}
	debugArtifactStat("stderr-create", stderrPath)

	collector := newOutputCollector(stdoutFile, stderrFile)

	runErr := runCodexExec(ctx, activeOpts, schemaPath, collector)
	if sanitizeErr := sanitizeCodexHomeOutput(activeOpts); sanitizeErr != nil {
		if runErr == nil {
			runErr = fmt.Errorf("sanitize codex home output: %w", sanitizeErr)
		} else {
			runErr = fmt.Errorf("%v; sanitize codex home output: %w", runErr, sanitizeErr)
		}
	}
	if persistErr := persistCodexHomeState(activeOpts.codexHomeStateOutboxPath(), activeOpts.CodexHome); persistErr != nil {
		if runErr == nil {
			runErr = fmt.Errorf("persist codex home state: %w", persistErr)
		} else {
			runErr = fmt.Errorf("%v; persist codex home state: %w", runErr, persistErr)
		}
	}
	if closeErr := stdoutFile.Close(); closeErr != nil {
		if runErr == nil {
			runErr = fmt.Errorf("close stdout capture: %w", closeErr)
		}
	}
	if closeErr := stderrFile.Close(); closeErr != nil {
		if runErr == nil {
			runErr = fmt.Errorf("close stderr capture: %w", closeErr)
		}
	}

	parseRes, parseErr := parseJSONL(stdoutPath)
	if parseErr != nil {
		return Result{}, "", "", "", parseErr
	}

	result := buildResultFromParse(parseRes)

	if runErr != nil {
		result.Status = StatusError
		if result.ErrorMessage == "" {
			result.ErrorMessage = runErr.Error()
		} else {
			result.ErrorMessage = fmt.Sprintf("%s; %v", result.ErrorMessage, runErr)
		}
	}

	if result.SessionID == "" {
		if parseRes.sessionID != "" {
			result.SessionID = parseRes.sessionID
		} else if activeOpts.SessionID != "" {
			result.SessionID = activeOpts.SessionID
		}
	}
	if sessionID := strings.TrimSpace(result.SessionID); sessionID != "" {
		if rolloutErr := materializeSessionRolloutFiles(activeOpts.CodexHome, sessionID, stdoutPath); rolloutErr != nil {
			if runErr == nil {
				runErr = fmt.Errorf("materialize session rollout files: %w", rolloutErr)
			} else {
				runErr = fmt.Errorf("%v; materialize session rollout files: %w", runErr, rolloutErr)
			}
		}
		if mappingErr := persistSessionCodexHomePath(sessionID, activeOpts.CodexHome); mappingErr != nil {
			if runErr == nil {
				runErr = fmt.Errorf("persist session codex home path: %w", mappingErr)
			} else {
				runErr = fmt.Errorf("%v; persist session codex home path: %w", runErr, mappingErr)
			}
		}
		if cacheErr := persistCodexHomeState(sessionCodexHomeStatePath(sessionID), activeOpts.CodexHome); cacheErr != nil {
			if runErr == nil {
				runErr = fmt.Errorf("persist session codex home state: %w", cacheErr)
			} else {
				runErr = fmt.Errorf("%v; persist session codex home state: %w", runErr, cacheErr)
			}
		}
	}

	return result, stdoutPath, stderrPath, artifactDir, nil
}

func resolveDirectCodexHome(opts Options) (string, error) {
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		return opts.CodexHome, nil
	}

	resolvedPath, err := loadSessionCodexHomePath(sessionID)
	if err != nil {
		return "", fmt.Errorf("load session codex home path: %w", err)
	}
	if strings.TrimSpace(resolvedPath) == "" {
		return opts.CodexHome, nil
	}
	return resolvedPath, nil
}

func runCodexExec(ctx context.Context, opts Options, schemaPath string, collector *outputCollector) error {
	if err := restoreCodexHomeStateIfExists(opts.codexHomeStateInboxPath(), opts.CodexHome); err != nil {
		return fmt.Errorf("restore codex home state: %w", err)
	}
	if sessionID := strings.TrimSpace(opts.SessionID); sessionID != "" {
		mappedHome, err := loadSessionCodexHomePath(sessionID)
		if err != nil {
			return fmt.Errorf("load session codex home path: %w", err)
		}
		if filepath.Clean(mappedHome) != filepath.Clean(opts.CodexHome) {
			if err := restoreCodexHomeStateIfExists(sessionCodexHomeStatePath(sessionID), opts.CodexHome); err != nil {
				return fmt.Errorf("restore session codex home state: %w", err)
			}
		}
	}
	if err := prepareDirectCodexHome(opts); err != nil {
		return fmt.Errorf("prepare codex home: %w", err)
	}
	cmdArgs := buildCommand(opts, schemaPath)
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	nullStdin, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open null stdin: %w", err)
	}
	defer nullStdin.Close()
	cmd.Env = os.Environ()
	for k, v := range buildEnv(opts) {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Dir = opts.WorktreeRoot
	cmd.Stdin = nullStdin
	cmd.Stdout = collector.stdoutWriter()
	cmd.Stderr = collector.stderrWriter()
	return runCommandWithWatchdog(ctx, cmd, opts.IdleTimeout, collector)
}

func runCommandWithWatchdog(ctx context.Context, cmd *exec.Cmd, idleTimeout time.Duration, collector *outputCollector) error {
	var lastActivity atomic.Int64
	recordActivity := func() {
		lastActivity.Store(time.Now().UnixNano())
	}
	recordActivity()
	collector.onActivity = recordActivity

	if err := cmd.Start(); err != nil {
		return err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	var idleTimer *time.Timer
	var idleC <-chan time.Time
	if idleTimeout > 0 {
		idleTimer = time.NewTimer(idleTimeout)
		defer idleTimer.Stop()
		idleC = idleTimer.C
	}

	for {
		select {
		case err := <-waitCh:
			return err
		case <-ctx.Done():
			terminateProcessGroup(cmd)
			<-waitCh
			return fmt.Errorf("codex execution canceled: %w", ctx.Err())
		case <-idleC:
			elapsed := time.Since(time.Unix(0, lastActivity.Load()))
			if elapsed >= idleTimeout {
				terminateProcessGroup(cmd)
				<-waitCh
				return fmt.Errorf("codex execution idle timeout after %s without stdout/stderr activity", idleTimeout)
			}
			idleTimer.Reset(idleTimeout - elapsed)
		}
	}
}

func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

func writeSchemaTempFile(schema []byte) (string, func(), error) {
	file, err := os.CreateTemp("", "codex-schema-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("create schema temp file: %w", err)
	}
	if _, err := file.Write(schema); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", nil, fmt.Errorf("write schema: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", nil, fmt.Errorf("close schema file: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(file.Name())
	}
	return file.Name(), cleanup, nil
}

func ensureExecutionPaths(opts Options) error {
	if err := os.MkdirAll(opts.WorktreeRoot, 0o755); err != nil {
		return fmt.Errorf("create worktree path: %w", err)
	}
	if err := os.MkdirAll(opts.ArtifactInbox, 0o755); err != nil {
		return fmt.Errorf("create inbox path: %w", err)
	}
	if err := os.MkdirAll(opts.ArtifactOutbox, 0o755); err != nil {
		return fmt.Errorf("create outbox path: %w", err)
	}
	if err := os.MkdirAll(opts.CodexHome, 0o755); err != nil {
		return fmt.Errorf("create codex home path: %w", err)
	}
	return nil
}

func sanitizeCodexHomeOutput(opts Options) error {
	return removeSensitiveCodexFiles(opts.CodexHome)
}

func removeSensitiveCodexFiles(codexHomeDir string) error {
	for _, name := range codexCredentialFileNames {
		targetPath := filepath.Join(codexHomeDir, name)
		if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove sensitive codex file %q: %w", targetPath, err)
		}
	}
	return nil
}

func prepareDirectCodexHome(opts Options) error {
	if err := ensureCodexHomeExists(opts.CodexHome); err != nil {
		return err
	}
	if err := installSkillSourcesIfExists(opts); err != nil {
		return err
	}
	if err := seedCodexCredentialsIfNeeded(opts.HostCodexHome, opts.CodexHome); err != nil {
		return err
	}
	return nil
}

func installSkillSourcesIfExists(opts Options) error {
	targetDir := opts.codexHomeSkillsPath()
	if err := os.RemoveAll(targetDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset codex skills %q: %w", targetDir, err)
	}
	for _, sourceDir := range opts.skillSourceDirs() {
		if err := copyDirContentsIfExists(sourceDir, targetDir); err != nil {
			return err
		}
	}
	return nil
}

func ensureCodexHomeExists(codexHomeDir string) error {
	if err := os.MkdirAll(codexHomeDir, 0o755); err != nil {
		return fmt.Errorf("create codex home %q: %w", codexHomeDir, err)
	}
	return nil
}

func restoreCodexHomeStateIfExists(sourceStateDir string, codexHomeDir string) error {
	info, err := os.Stat(sourceStateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat inbox codex home %q: %w", sourceStateDir, err)
	}
	if !info.IsDir() {
		return nil
	}
	if filepath.Clean(sourceStateDir) == filepath.Clean(codexHomeDir) {
		return nil
	}
	if err := os.RemoveAll(codexHomeDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset codex home %q: %w", codexHomeDir, err)
	}
	return copyDirContents(sourceStateDir, codexHomeDir)
}

func persistCodexHomeState(targetStateDir string, codexHomeDir string) error {
	if err := os.RemoveAll(targetStateDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset codex home state %q: %w", targetStateDir, err)
	}
	return copyDirContentsIfExists(codexHomeDir, targetStateDir)
}

func sessionCodexHomeStatePath(sessionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	return filepath.Join(os.TempDir(), "colony2-codex-session-state", hex.EncodeToString(sum[:]))
}

func sessionCodexHomePathRecord(sessionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	return filepath.Join(os.TempDir(), "colony2-codex-session-homes", hex.EncodeToString(sum[:]), "path.txt")
}

func persistSessionCodexHomePath(sessionID string, codexHome string) error {
	recordPath := sessionCodexHomePathRecord(sessionID)
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o755); err != nil {
		return fmt.Errorf("create session codex home record dir: %w", err)
	}
	return os.WriteFile(recordPath, []byte(strings.TrimSpace(codexHome)), 0o644)
}

func loadSessionCodexHomePath(sessionID string) (string, error) {
	recordPath := sessionCodexHomePathRecord(sessionID)
	data, err := os.ReadFile(recordPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read session codex home record: %w", err)
	}
	path := strings.TrimSpace(string(data))
	if path == "" {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat session codex home: %w", err)
	}
	if !info.IsDir() {
		return "", nil
	}
	return path, nil
}

func materializeSessionRolloutFiles(codexHomeDir string, sessionID string, stdoutPath string) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(stdoutPath) == "" {
		return nil
	}
	paths, err := discoverSessionRolloutPaths(codexHomeDir, sessionID)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("stat rollout path %q: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create rollout dir %q: %w", filepath.Dir(path), err)
		}
		if err := copyRegularFile(stdoutPath, path, 0o644); err != nil {
			return fmt.Errorf("copy rollout file %q: %w", path, err)
		}
	}
	return nil
}

func discoverSessionRolloutPaths(codexHomeDir string, sessionID string) ([]string, error) {
	patterns := []string{
		filepath.Join(codexHomeDir, "state_*.sqlite"),
		filepath.Join(codexHomeDir, "state_*.sqlite-*"),
	}
	seenFiles := map[string]struct{}{}
	seenPaths := map[string]struct{}{}
	paths := make([]string, 0)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("glob state dbs %q: %w", pattern, err)
		}
		for _, match := range matches {
			if _, ok := seenFiles[match]; ok {
				continue
			}
			seenFiles[match] = struct{}{}
			data, err := os.ReadFile(match)
			if err != nil {
				return nil, fmt.Errorf("read state db %q: %w", match, err)
			}
			for _, chunk := range bytes.Split(data, []byte{0}) {
				candidate := extractRolloutPathCandidate(string(chunk), sessionID)
				if candidate == "" {
					continue
				}
				if _, ok := seenPaths[candidate]; ok {
					continue
				}
				seenPaths[candidate] = struct{}{}
				paths = append(paths, candidate)
			}
		}
	}
	return paths, nil
}

func extractRolloutPathCandidate(raw string, sessionID string) string {
	idx := strings.Index(raw, string(filepath.Separator))
	if idx == -1 {
		return ""
	}
	raw = raw[idx:]
	if !strings.Contains(raw, sessionID) {
		return ""
	}
	sessionFragment := string(filepath.Separator) + ".codex" + string(filepath.Separator) + "sessions" + string(filepath.Separator)
	if !strings.Contains(raw, sessionFragment) {
		return ""
	}
	end := strings.Index(raw, ".jsonl")
	if end == -1 {
		return ""
	}
	candidate := raw[:end+len(".jsonl")]
	if !filepath.IsAbs(candidate) {
		return ""
	}
	return candidate
}

func copyDirContentsIfExists(sourceDir string, targetDir string) error {
	if filepath.Clean(sourceDir) == filepath.Clean(targetDir) {
		return nil
	}
	info, err := os.Stat(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat inbox codex home %q: %w", sourceDir, err)
	}
	if !info.IsDir() {
		return nil
	}
	return copyDirContents(sourceDir, targetDir)
}

func copyDirContents(sourceDir string, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create target directory %q: %w", targetDir, err)
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("read directory %q: %w", sourceDir, err)
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(sourceDir, entry.Name())
		targetPath := filepath.Join(targetDir, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat source path %q: %w", sourcePath, err)
		}

		if info.IsDir() {
			if err := copyDirContents(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}

		if !info.Mode().IsRegular() {
			continue
		}
		if err := copyRegularFile(sourcePath, targetPath, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func seedCodexCredentialsIfNeeded(sourceHomeDir string, targetHomeDir string) error {
	sourceHomeDir = strings.TrimSpace(sourceHomeDir)
	if sourceHomeDir == "" {
		return nil
	}
	info, err := os.Stat(sourceHomeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat host codex home %q: %w", sourceHomeDir, err)
	}
	if !info.IsDir() {
		return nil
	}
	if err := os.MkdirAll(targetHomeDir, 0o755); err != nil {
		return fmt.Errorf("create codex home %q: %w", targetHomeDir, err)
	}

	for _, name := range codexCredentialFileNames {
		sourcePath := filepath.Join(sourceHomeDir, name)
		targetPath := filepath.Join(targetHomeDir, name)
		if _, err := os.Stat(targetPath); err == nil {
			continue
		}

		sourceInfo, err := os.Stat(sourcePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat credential source %q: %w", sourcePath, err)
		}
		if !sourceInfo.Mode().IsRegular() {
			continue
		}
		if err := copyRegularFile(sourcePath, targetPath, sourceInfo.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(sourcePath string, targetPath string, mode os.FileMode) error {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source file %q: %w", sourcePath, err)
	}
	defer sourceFile.Close()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create target parent %q: %w", filepath.Dir(targetPath), err)
	}
	if mode == 0 {
		mode = 0o644
	}
	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("open target file %q: %w", targetPath, err)
	}
	defer targetFile.Close()

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return fmt.Errorf("copy %q to %q: %w", sourcePath, targetPath, err)
	}
	return nil
}

func buildResultFromParse(outcome parseOutcome) Result {
	res := Result{}

	if outcome.payload == nil {
		res.Status = StatusError
		res.ErrorMessage = fallbackErrorMessage(outcome)
		return res
	}

	switch strings.ToLower(outcome.payload.Status) {
	case string(StatusCompleted):
		res.Status = StatusCompleted
	case string(StatusIncomplete):
		res.Status = StatusIncomplete
	case string(StatusError):
		res.Status = StatusError
	default:
		res.Status = StatusError
		res.ErrorMessage = fmt.Sprintf("unknown status %q", outcome.payload.Status)
	}

	res.SessionID = outcome.sessionID
	res.AssistantSummary = outcome.payload.AssistantSummary
	res.IncompleteReason = outcome.payload.IncompleteReason
	res.IncompleteCategory = outcome.payload.IncompleteCategory
	res.PendingDependencies = outcome.payload.PendingDeps
	if len(res.PendingDependencies) == 0 && res.Status == StatusIncomplete && res.IncompleteCategory == "" {
		res.IncompleteCategory = "agent_abandoned"
	}
	if outcome.payload.ErrorMessage != "" {
		res.ErrorMessage = outcome.payload.ErrorMessage
	} else if outcome.failureMessage != "" && res.Status == StatusError {
		res.ErrorMessage = outcome.failureMessage
	}
	return res
}

func fallbackErrorMessage(outcome parseOutcome) string {
	if outcome.failureMessage != "" {
		return outcome.failureMessage
	}
	return "codex did not produce structured output"
}
