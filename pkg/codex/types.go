package codex

import (
	"context"
	"time"

	"github.com/colony-2/shai/pkg/shai"
)

// Status describes the overall Codex execution outcome.
type Status string

const (
	StatusCompleted  Status = "completed"
	StatusIncomplete Status = "incomplete"
	StatusError      Status = "error"
)

// Dependency captures follow-up work Codex requests for dependent components.
type Dependency struct {
	Component        string `json:"component"`
	RequestedChanges string `json:"requestedChanges"`
}

// Result represents the normalized Codex execution outcome returned by Execute.
type Result struct {
	Status              Status       `json:"status"`
	SessionID           string       `json:"sessionId"`
	AssistantSummary    string       `json:"assistantSummary,omitempty"`
	IncompleteReason    string       `json:"incompleteReason,omitempty"`
	IncompleteCategory  string       `json:"incompleteCategory,omitempty"`
	PendingDependencies []Dependency `json:"pendingDependencies,omitempty"`
	ErrorMessage        string       `json:"errorMessage,omitempty"`
}

// Clock abstracts time retrieval for deterministic testing.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// Runner represents the minimal surface needed from a Shai runner.
type Runner interface {
	Run(ctx context.Context) error
	Close() error
}

// RunnerFactory constructs runners from the generated EphemeralConfig.
type RunnerFactory func(config *shai.SandboxConfig) (Runner, error)

// Options control Execute behaviour.
type Options struct {
	Prompt    string
	SessionID string
	Model     string
	ExtraEnv  map[string]string

	WorkDirRoot         string
	WorktreeRoot        string
	CellRelativePath    string
	ArtifactInbox       string
	ArtifactOutbox      string
	CodexHome           string
	HostCodexHome       string
	ConfiguredSkillDirs []string

	Clock            Clock
	RunnerFactory    RunnerFactory
	StructuredSchema []byte
}
