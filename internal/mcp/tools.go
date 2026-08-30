package mcp

import "encoding/json"

// EditorTools returns the editor tools tend advertises to an agent (see
// docs/adr/0004): read_buffer, open_buffer, write_buffer, edit_buffer.
//
// write_buffer and edit_buffer are SUPERVISED: they run through the daemon's
// change-set -> approval flow, so proposing one asks for the user's approval
// (they review a diff) rather than writing directly. read_buffer and open_buffer
// are non-mutating.
func EditorTools() []Tool {
	return []Tool{
		{
			Name:        "read_buffer",
			Title:       "Read buffer",
			Description: "Read a file as the user's editor sees it — the live buffer (INCLUDING unsaved edits) when the file is open, otherwise disk. Prefer this over reading the file from disk directly (native file-read / cat) for any file the user may be editing, so you see the current buffer state rather than a stale on-disk copy.",
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
			Description: "Open a file in the user's editor (Neovim, VS Code, …) so it appears as a buffer/tab. Use this whenever the user asks to open, show, or bring up a file in their editor — do NOT shell out to the editor (e.g. `nvim --remote`) or probe for editor sockets; this is the supported way. Non-mutating; a no-op when the session has no editor attached.",
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
			Name:        "write_buffer",
			Title:       "Write buffer",
			Description: "Propose replacing a file's ENTIRE content as a SUPERVISED edit through the user's editor: they review a diff and it applies to the live buffer only on approval — not a direct write. Prefer this over writing the file directly (native file-write) for any file the user is editing, so the change lands in their live buffer and is reviewed as a diff. Use edit_buffer for targeted changes to a large file.",
			InputSchema: schema(`{
				"type": "object",
				"properties": {
					"uri": { "type": "string", "description": "file:// URI of the file to write" },
					"new_text": { "type": "string", "description": "the file's full proposed new content" }
				},
				"required": ["uri", "new_text"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        "edit_buffer",
			Title:       "Edit buffer",
			Description: "Propose targeted, non-overlapping text edits to a file as a SUPERVISED edit through the user's editor. Each edit replaces the text in a [start, end) range with new_text; positions are 0-indexed line and byte column. The user reviews the resulting diff and it applies to the live buffer only on approval. Prefer this over editing the file directly (native edit) for any file the user is editing. Read the file first (read_buffer) so your positions match its current content.",
			InputSchema: schema(`{
				"type": "object",
				"properties": {
					"uri": { "type": "string", "description": "file:// URI of the file to edit" },
					"edits": {
						"type": "array",
						"description": "non-overlapping edits applied together",
						"items": {
							"type": "object",
							"properties": {
								"start_line": { "type": "integer", "description": "0-indexed start line" },
								"start_column": { "type": "integer", "description": "0-indexed start byte column" },
								"end_line": { "type": "integer", "description": "0-indexed end line (exclusive of the range end)" },
								"end_column": { "type": "integer", "description": "0-indexed end byte column" },
								"new_text": { "type": "string", "description": "replacement text for the range" }
							},
							"required": ["start_line", "start_column", "end_line", "end_column", "new_text"],
							"additionalProperties": false
						}
					}
				},
				"required": ["uri", "edits"],
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
