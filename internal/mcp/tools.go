package mcp

import "encoding/json"

// EditorTools returns the editor tools tend advertises to an agent (see
// docs/adr/0004). The schemas are the agent-facing contract; the handlers that
// route each call to the daemon's editor/file services are wired separately.
// read/open are plain reads; edit is a supervised change (it will run through
// the change-set -> approval flow), so an agent proposing an edit is asking for
// the user's approval, not writing directly.
func EditorTools() []Tool {
	return []Tool{
		{
			Name:        "read_buffer",
			Title:       "Read buffer",
			Description: "Read a file's current content as the editor sees it (the live buffer when the file is open, otherwise disk). Prefer this over reading disk directly so you see unsaved edits.",
			InputSchema: schema(`{
				"type": "object",
				"properties": {
					"uri": { "type": "string", "description": "file:// URI of the file to read" }
				},
				"required": ["uri"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        "open_buffer",
			Title:       "Open buffer",
			Description: "Open a file in the user's editor so they can see it. Does not modify the file.",
			InputSchema: schema(`{
				"type": "object",
				"properties": {
					"uri": { "type": "string", "description": "file:// URI of the file to open" }
				},
				"required": ["uri"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        "edit_buffer",
			Title:       "Edit buffer",
			Description: "Propose replacing a file's content. The change is shown to the user as a diff and applied to the live buffer only after they approve it — this is a supervised edit, not a direct write.",
			InputSchema: schema(`{
				"type": "object",
				"properties": {
					"uri": { "type": "string", "description": "file:// URI of the file to edit" },
					"new_text": { "type": "string", "description": "the file's full proposed new content" }
				},
				"required": ["uri", "new_text"],
				"additionalProperties": false
			}`),
		},
	}
}

// schema parses a JSON-Schema literal into a RawMessage, panicking on a
// malformed literal — the schemas are compile-time constants, so a bad one is a
// programmer error that should fail fast in tests.
func schema(s string) json.RawMessage {
	var v json.RawMessage
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("mcp: invalid tool schema literal: " + err.Error())
	}
	return v
}
