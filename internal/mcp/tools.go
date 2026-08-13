package mcp

import "encoding/json"

// EditorTools returns the editor tools tend advertises to an agent (see
// docs/adr/0004). tools/list should only advertise tools that are actually
// callable, so the set grows as each tool's handler is wired to the daemon.
// Currently: read_buffer. open_buffer (needs a plugin->daemon open method) and
// the supervised edit_buffer (change-set -> approval flow) join here as they
// land.
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
