package codex

import "time"

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

// Options control Execute behaviour.
type Options struct {
	Prompt    string
	SessionID string
	Model     string
	ExtraEnv  map[string]string

	IdleTimeout time.Duration

	WorkDirRoot         string
	WorktreeRoot        string
	CellRelativePath    string
	ArtifactInbox       string
	ArtifactOutbox      string
	CodexHome           string
	HostCodexHome       string
	ConfiguredSkillDirs []string

	StructuredSchema []byte
}
