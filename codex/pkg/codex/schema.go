package codex

var structuredOutputSchema = []byte(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "status",
    "assistantSummary",
    "incompleteReason",
    "incompleteCategory",
    "pendingDependencies",
    "errorMessage"
  ],
  "properties": {
    "status": {
      "type": "string",
      "enum": ["completed", "incomplete", "error"]
    },
    "assistantSummary": { "type": "string" },
    "incompleteReason": { "type": "string" },
    "incompleteCategory": {
      "type": "string",
      "enum": ["dependency_blockers", "agent_abandoned", ""]
    },
    "pendingDependencies": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["component", "requestedChanges"],
        "additionalProperties": false,
        "properties": {
          "component": { "type": "string" },
          "requestedChanges": { "type": "string" }
        }
      }
    },
    "errorMessage": { "type": "string" }
  }
}`)

// StructuredOutputSchema exposes the canonical schema for consumers needing direct access.
var StructuredOutputSchema = make([]byte, len(structuredOutputSchema))

func init() {
	copy(StructuredOutputSchema, structuredOutputSchema)
}
