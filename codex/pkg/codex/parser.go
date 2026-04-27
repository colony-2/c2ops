package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type codexEvent struct {
	Type      string      `json:"type"`
	SessionID string      `json:"session_id"`
	ThreadID  string      `json:"thread_id"`
	Error     *eventError `json:"error"`
	Item      *eventItem  `json:"item"`
}

type eventError struct {
	Message string `json:"message"`
}

type eventItem struct {
	ID       string             `json:"id"`
	ItemType string             `json:"item_type"`
	Type     string             `json:"type"`
	Text     string             `json:"text"`
	Content  []eventItemContent `json:"content"`
}

type eventItemContent struct {
	Type string          `json:"type"`
	Text string          `json:"text"`
	JSON json.RawMessage `json:"json"`
}

type assistantPayload struct {
	Status             string       `json:"status"`
	AssistantSummary   string       `json:"assistantSummary"`
	IncompleteReason   string       `json:"incompleteReason"`
	IncompleteCategory string       `json:"incompleteCategory"`
	PendingDeps        []Dependency `json:"pendingDependencies"`
	ErrorMessage       string       `json:"errorMessage"`
}

type parseOutcome struct {
	sessionID      string
	payload        *assistantPayload
	failureMessage string
}

func parseJSONL(path string) (parseOutcome, error) {
	f, err := os.Open(path)
	if err != nil {
		return parseOutcome{}, fmt.Errorf("open stdout: %w", err)
	}
	defer f.Close()

	var outcome parseOutcome
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 8*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt codexEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue // ignore non-JSON output
		}
		switch evt.Type {
		case "session.created", "thread.started":
			if evt.SessionID != "" {
				outcome.sessionID = evt.SessionID
			} else if evt.ThreadID != "" {
				outcome.sessionID = evt.ThreadID
			}
		case "turn.failed", "error":
			if evt.Error != nil && evt.Error.Message != "" {
				outcome.failureMessage = evt.Error.Message
			}
		case "item.completed":
			if evt.Item == nil {
				continue
			}
			itemType := evt.Item.ItemType
			if itemType == "" {
				itemType = evt.Item.Type
			}
			if itemType == "assistant_message" || itemType == "agent_message" {
				payload, err := decodeAssistantPayload(evt.Item)
				if err != nil {
					outcome.failureMessage = err.Error()
					continue
				}
				outcome.payload = payload
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return parseOutcome{}, fmt.Errorf("scan stdout: %w", err)
	}

	return outcome, nil
}

func decodeAssistantPayload(item *eventItem) (*assistantPayload, error) {
	raw := strings.TrimSpace(item.Text)
	if raw == "" {
		for _, c := range item.Content {
			if c.Type == "output_json" && len(c.JSON) > 0 {
				raw = string(c.JSON)
				break
			}
			if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
				raw = c.Text
				break
			}
		}
	}
	if raw == "" {
		return nil, fmt.Errorf("assistant message missing structured payload")
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") {
		if unquoted, err := strconv.Unquote(trimmed); err == nil {
			trimmed = unquoted
		}
	}

	var payload assistantPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("parse assistant payload: %w", err)
	}

	if payload.PendingDeps == nil {
		payload.PendingDeps = []Dependency{}
	}
	if strings.TrimSpace(payload.Status) == "" {
		return nil, fmt.Errorf("assistant payload missing status")
	}
	if strings.TrimSpace(payload.AssistantSummary) == "" {
		return nil, fmt.Errorf("assistant payload missing assistantSummary")
	}

	return &payload, nil
}
