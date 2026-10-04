package codex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2ops/codex/pkg/checkpoint"
	_ "modernc.org/sqlite"
)

const SessionObjectType = "c2ops.codex.session/v1"
const supportedCodexVersion = "0.157.1"
const sessionStateFormat = "codex-0.157.1/v1"

type SessionMetadata struct {
	SessionID      string `json:"session_id"`
	RuntimeVersion string `json:"runtime_version"`
	StateFormat    string `json:"state_format"`
}

// SessionInput is c2j's hydrated checkpoint, not a bare recipe reference.
type SessionInput struct {
	Ref      objects.Ref       `json:"ref"`
	Metadata SessionMetadata   `json:"metadata"`
	Files    map[string]string `json:"files"`
}

type sessionExecution struct {
	ctx    context.Context
	home   string
	id     string
	outbox string
}

// The supported CLI creates all these databases in a private sqlite_home.
// Logs, credentials, installed skills, caches and locks are deliberately excluded.
var sessionDatabases = []string{
	"state_5.sqlite", "thread_history_1.sqlite", "goals_1.sqlite",
	"queue_1.sqlite", "memories_1.sqlite",
}
var sessionDirectories = []string{"sessions", "archived_sessions", "memories"}

func prepareSession(ctx context.Context, input *SessionInput, paths execRunPaths, env map[string]string) (*sessionExecution, error) {
	outbox := os.Getenv("C2J_OBJECT_OUTBOX")
	if !filepath.IsAbs(outbox) {
		return nil, fmt.Errorf("C2J_OBJECT_OUTBOX must be provided by an object-capable c2j runtime")
	}
	for _, key := range []string{"CODEX_HOME", "CODEX_SQLITE_HOME", "C2J_OBJECT_OUTBOX"} {
		if _, ok := env[key]; ok {
			return nil, fmt.Errorf("env.%s is managed by the session object", key)
		}
	}
	if _, err := os.Lstat(filepath.Join(paths.Inbox, legacyHomeArtifact)); err == nil {
		return nil, fmt.Errorf("codex-home-state artifacts are no longer supported; pass a session object")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	progress(ctx, "version.check", nil)
	version, err := readCodexVersion(ctx, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("read Codex version: %w", err)
	}
	if strings.TrimSpace(string(version)) != "codex-cli "+supportedCodexVersion {
		return nil, fmt.Errorf("session checkpoints require codex-cli %s; got %q", supportedCodexVersion, strings.TrimSpace(string(version)))
	}
	progress(ctx, "version.ready", map[string]any{"version": supportedCodexVersion})
	if err := os.MkdirAll(paths.Workdir, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outbox, 0700); err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp(paths.Workdir, ".codex-session-")
	if err != nil {
		return nil, err
	}
	s := &sessionExecution{ctx: ctx, home: home, outbox: outbox}
	if input == nil {
		progress(ctx, "session.new", nil)
		return s, nil
	}
	progress(ctx, "session.restore", nil)
	if err := s.restore(input, paths.Worktree); err != nil {
		os.RemoveAll(home)
		return nil, fmt.Errorf("restore session: %w", err)
	}
	progress(ctx, "session.ready", nil)
	return s, nil
}

func (s *sessionExecution) restore(input *SessionInput, worktree string) error {
	if err := input.Ref.Validate(); err != nil {
		return err
	}
	if input.Ref.Type != SessionObjectType {
		return fmt.Errorf("expected %s", SessionObjectType)
	}
	m := input.Metadata
	if m.SessionID == "" || m.RuntimeVersion != supportedCodexVersion || m.StateFormat != sessionStateFormat {
		return fmt.Errorf("invalid or unsupported session metadata")
	}
	home := input.Files["home"]
	if len(input.Files) != 1 || !filepath.IsAbs(home) {
		return fmt.Errorf("session requires one absolute files.home directory")
	}
	if err := validateStateTree(home); err != nil {
		return err
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !containsName(sessionDirectories, entry.Name()) && !containsName(sessionDatabases, entry.Name()) {
			return fmt.Errorf("unsupported checkpoint file %q", entry.Name())
		}
	}
	if err := copyDirContents(home, s.home); err != nil {
		return err
	}
	for _, name := range sessionDatabases {
		path := filepath.Join(s.home, name)
		if err := requireRegularFile(path); err != nil {
			return err
		}
		db, err := openStateDB(path)
		if err != nil {
			return err
		}
		var check string
		err = db.QueryRow("PRAGMA quick_check").Scan(&check)
		db.Close()
		if err != nil {
			return err
		}
		if check != "ok" {
			return fmt.Errorf("invalid checkpoint database %s: %s", name, check)
		}
	}
	if err := relocateSessionIndex(s.home, m.SessionID, worktree, false); err != nil {
		return err
	}
	s.id = m.SessionID
	return nil
}

func (s *sessionExecution) close() { _ = os.RemoveAll(s.home) }

func (s *sessionExecution) publish() (*checkpoint.Marker, map[string]checkpoint.Draft, error) {
	progress(s.ctx, "session.export", nil)
	if s.id == "" {
		return nil, nil, fmt.Errorf("Codex returned no session ID")
	}
	dir, err := os.MkdirTemp(s.outbox, "codex-session-")
	if err != nil {
		return nil, nil, err
	}
	// Export files must outlive this process. Only remove an unsuccessful draft.
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()
	home := filepath.Join(dir, "home")
	if err := exportSessionHome(s.home, home); err != nil {
		return nil, nil, fmt.Errorf("export session: %w", err)
	}
	if err := relocateSessionIndex(home, s.id, "", true); err != nil {
		return nil, nil, fmt.Errorf("finalize session: %w", err)
	}
	success = true
	progress(s.ctx, "session.exported", nil)
	return &checkpoint.Marker{Name: "session"}, map[string]checkpoint.Draft{
		"session": {Type: SessionObjectType, Metadata: SessionMetadata{s.id, supportedCodexVersion, sessionStateFormat}, Files: map[string]string{"home": home}},
	}, nil
}

func exportSessionHome(source, target string) error {
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".sqlite") && name != "logs_2.sqlite" && !containsName(sessionDatabases, name) {
			return fmt.Errorf("unsupported Codex database %q", name)
		}
	}
	for _, name := range sessionDirectories {
		src := filepath.Join(source, name)
		if _, err := os.Lstat(src); os.IsNotExist(err) {
			continue
		}
		if err := validateStateTree(src); err != nil {
			return err
		}
		if err := copyDirContents(src, filepath.Join(target, name)); err != nil {
			return err
		}
	}
	for _, name := range sessionDatabases {
		src := filepath.Join(source, name)
		if err := requireRegularFile(src); err != nil {
			return err
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := requireRegularFile(src + suffix); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		// SQLite reads the WAL and VACUUM INTO creates a self-contained database.
		db, err := openStateDB(src)
		if err != nil {
			return err
		}
		_, exportErr := db.Exec("VACUUM INTO ?", filepath.Join(target, name))
		closeErr := db.Close()
		if exportErr != nil {
			return exportErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	// Make the absolute runtime paths portable in the exported database only.
	db, err := openStateDB(filepath.Join(target, "state_5.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query("SELECT id, rollout_path FROM threads")
	if err != nil {
		return err
	}
	type entry struct{ id, path string }
	var paths []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.path); err != nil {
			rows.Close()
			return err
		}
		if filepath.IsAbs(e.path) {
			if err := ensureDescendantPath(source, e.path, "rollout"); err != nil {
				rows.Close()
				return err
			}
			e.path, err = filepath.Rel(source, e.path)
			if err != nil {
				rows.Close()
				return err
			}
		}
		paths = append(paths, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range paths {
		if _, err := db.Exec("UPDATE threads SET rollout_path = ?, cwd = '' WHERE id = ?", filepath.ToSlash(e.path), e.id); err != nil {
			return err
		}
	}
	_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Stored rollout paths are relative. Only the invocation copy receives absolute paths.
func relocateSessionIndex(home, sessionID, worktree string, exporting bool) error {
	for _, name := range sessionDatabases {
		if err := requireRegularFile(filepath.Join(home, name)); err != nil {
			return err
		}
	}
	db, err := openStateDB(filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query("SELECT id, rollout_path FROM threads")
	if err != nil {
		return err
	}
	type entry struct{ id, path string }
	var entries []entry
	found := false
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.path); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
		found = found || e.id == sessionID
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("session %q is absent from the state database", sessionID)
	}
	for _, e := range entries {
		clean := filepath.ToSlash(filepath.Clean(e.path))
		if filepath.IsAbs(e.path) || !(strings.HasPrefix(clean, "sessions/") || strings.HasPrefix(clean, "archived_sessions/")) {
			return fmt.Errorf("invalid checkpoint rollout path %q", e.path)
		}
		path := filepath.Join(home, filepath.FromSlash(clean))
		if err := requireRegularFile(path); err != nil {
			return err
		}
		if !exporting {
			if _, err := db.Exec("UPDATE threads SET rollout_path = ?, cwd = ? WHERE id = ?", path, worktree, e.id); err != nil {
				return err
			}
		}
	}
	_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

func openStateDB(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=rw")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("session file must be regular: %s", path)
	}
	return nil
}

func validateStateTree(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("session part must be a directory: %s", root)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		return requireRegularFile(path)
	})
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if name == n {
			return true
		}
	}
	return false
}

// Reject removed inputs even when callers decode structs without extensioncmd.
func rejectLegacySessionFields(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"sessionId", "resume_context"} {
		if _, ok := fields[name]; ok {
			return fmt.Errorf("%s is no longer supported; use session with a c2j object reference", name)
		}
	}
	if raw, ok := fields["session"]; ok && string(raw) == "null" {
		return fmt.Errorf("session must be an object; omit it to start fresh")
	}
	return nil
}

// Version probing happens before the execution idle watchdog starts.
func readCodexVersion(ctx context.Context, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "--version")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error { terminateProcessGroup(cmd); return nil }
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Codex version check: %w", ctx.Err())
	}
	return output, err
}
