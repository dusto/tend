package api

// Params/result types for daemon->editor methods (implemented by the bound editor).

// EditorReadBufferParams requests an editor-aware read of a file.
type EditorReadBufferParams struct {
	URI string `json:"uri"`
}

// EditorReadBufferResult returns file content and the base revision it reflects.
type EditorReadBufferResult struct {
	Content string   `json:"content"`
	Base    FileBase `json:"base"`
	// Open is true when the content was served from a live buffer rather than disk.
	Open bool `json:"open"`
}

// FileBase identifies the revision a read/edit was computed against, so a later
// patch's conflict check can verify it. Exactly one field is set: ChangedTick
// for an open buffer, ContentHash for a closed file.
type FileBase struct {
	ContentHash string `json:"content_hash,omitempty"`
	ChangedTick *int64 `json:"changedtick,omitempty"`
}
