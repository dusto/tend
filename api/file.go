package api

// Params/result types for the plugin->daemon file methods.

// TextEdit replaces the bytes spanned by Range with NewText. The edits in a
// patch are expressed against the base snapshot and must be non-overlapping; see
// the patch package for application semantics and the {line, byte_col} position
// unit.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"new_text"`
}

// FileReadParams reads a repo file editor-aware. SessionID selects the session
// whose bound editor (if any) provides live buffer content.
type FileReadParams struct {
	SessionID SessionID `json:"session_id"`
	URI       string    `json:"uri"`
}

// FileReadResult returns the file content and the base revision it reflects, so
// a follow-up patch can cite the exact revision its conflict check will verify.
// Open is true when the content came from a live editor buffer (base is a
// changedtick); otherwise it came from disk (base is a content hash).
type FileReadResult struct {
	Content string   `json:"content"`
	Base    FileBase `json:"base"`
	Open    bool     `json:"open"`
}
