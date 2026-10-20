package codex

import (
	"encoding/json"
	"time"
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

// NativeOutput retains Codex's event fields without imposing a model response envelope.
// Events are lossless JSON objects; Items contains the latest snapshot per item per turn.
type NativeOutput struct {
	Response             json.RawMessage   `json:"response"`
	ResponseText         string            `json:"response_text"`
	Events               []json.RawMessage `json:"events"`
	Items                []json.RawMessage `json:"items"`
	Usage                json.RawMessage   `json:"usage"`
	TerminalEvent        json.RawMessage   `json:"terminal_event"`
	ExitCode             *int              `json:"exit_code"`
	OutputSchemaValid    *bool             `json:"output_schema_valid,omitempty"`
	OutputSchemaErrors   []string          `json:"output_schema_errors,omitempty"`
	OutputRepairAttempts int               `json:"output_repair_attempts"`
	SchemaMode           string            `json:"schema_mode,omitempty"`
}

// Result describes execution, independently of the content of the model's answer.
type Result struct {
	NativeOutput
	Status       Status `json:"status"`
	SessionID    string `json:"sessionId"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// ResponseOutputSpec requests validation of the model's final answer.
type ResponseOutputSpec struct {
	Schema            json.RawMessage `json:"schema,omitempty"`
	MaxRepairAttempts *int            `json:"max_repair_attempts,omitempty"`
}

// Options control Execute behaviour.
type Options struct {
	Prompt    string
	SessionID string
	Model     string
	API       *APIConfig
	ExtraEnv  map[string]string

	IdleTimeout time.Duration

	WorkDirRoot         string
	WorktreeRoot        string
	ArtifactInbox       string
	ArtifactOutbox      string
	CodexHome           string
	HostCodexHome       string
	ConfiguredSkillDirs []string

	StructuredSchema  []byte
	MaxRepairAttempts *int
}
