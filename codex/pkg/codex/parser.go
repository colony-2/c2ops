package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Decode only the fields needed for indexing; retain the original objects so
// new Codex fields and event types are available without a wrapper upgrade.
type codexEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	ThreadID  string          `json:"thread_id"`
	Message   string          `json:"message"`
	Error     *eventError     `json:"error"`
	Item      json.RawMessage `json:"item"`
	Usage     json.RawMessage `json:"usage"`
}
type eventError struct {
	Message string `json:"message"`
}
type eventItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	ItemType string `json:"item_type"`
	Text     string `json:"text"`
}
type parseOutcome struct{ Result }

func parseJSONL(path string) (parseOutcome, error) {
	f, err := os.Open(path)
	if err != nil {
		return parseOutcome{}, fmt.Errorf("open stdout: %w", err)
	}
	defer f.Close()
	out := parseOutcome{Result: Result{NativeOutput: NativeOutput{
		Events: []json.RawMessage{}, Items: []json.RawMessage{},
	}, Status: StatusError}}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	itemPositions := map[string]int{}
	lastError := ""
	terminal := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		var evt codexEvent
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &evt) != nil {
			continue
		}
		raw := json.RawMessage(line)
		out.Events = append(out.Events, raw)
		switch evt.Type {
		case "thread.started", "session.created":
			out.SessionID = firstNonEmpty(evt.ThreadID, evt.SessionID)
		case "turn.started":
			// Item ids may be reused by resumed invocations. Never conflate turns.
			itemPositions = map[string]int{}
			out.ResponseText = ""
			out.Response = nil
			out.TerminalEvent = nil
			out.Usage = nil
			out.Status = StatusError
			out.ErrorMessage = ""
			terminal = false
			lastError = ""
		case "error":
			lastError = evt.Message
			if evt.Error != nil {
				lastError = firstNonEmpty(evt.Error.Message, lastError)
			}
			// Reconnection errors can be followed by successful completion.
		case "turn.completed", "turn.failed":
			terminal = true
			out.TerminalEvent = raw
			out.Usage = evt.Usage
			if evt.Type == "turn.completed" {
				out.Status = StatusCompleted
				out.ErrorMessage = ""
			} else {
				out.Status = StatusError
				if evt.Error != nil {
					out.ErrorMessage = evt.Error.Message
				}
				out.ErrorMessage = firstNonEmpty(out.ErrorMessage, lastError, "Codex turn failed")
			}
		}
		if strings.HasPrefix(evt.Type, "item.") && len(evt.Item) > 0 && string(evt.Item) != "null" {
			var item eventItem
			if err := json.Unmarshal(evt.Item, &item); err != nil {
				continue
			}
			if pos, exists := itemPositions[item.ID]; item.ID != "" && exists {
				out.Items[pos] = evt.Item
			} else {
				if item.ID != "" {
					itemPositions[item.ID] = len(out.Items)
				}
				out.Items = append(out.Items, evt.Item)
			}
			kind := firstNonEmpty(item.Type, item.ItemType)
			if evt.Type == "item.completed" && (kind == "agent_message" || kind == "assistant_message") {
				out.ResponseText = item.Text
				out.Response, _ = json.Marshal(item.Text)
			}
		}
	}
	if !terminal {
		out.Status = StatusError
		out.ErrorMessage = firstNonEmpty(lastError, "Codex stream ended without a terminal turn event")
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scan stdout: %w", err)
	}
	return out, nil
}
