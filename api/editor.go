package api

// Params/result types for daemon->editor methods (implemented by the bound editor).

// Position is a location in a buffer: a 0-based line index and a byte offset
// within that line's UTF-8 bytes — not a UTF-16 code unit and not a rune index.
// This is the unambiguous column unit the file-mutation and selection contracts
// share.
type Position struct {
	Line    int `json:"line"`
	ByteCol int `json:"byte_col"`
}

// Range is a half-open span [Start, End) within a buffer.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// EditorCurrentBufferParams requests the editor's active buffer.
type EditorCurrentBufferParams struct{}

// EditorCurrentBufferResult reports the editor's active buffer. URI is empty
// when no file-backed buffer is active.
type EditorCurrentBufferResult struct {
	URI string `json:"uri"`
}

// EditorSelectionParams requests the editor's current selection.
type EditorSelectionParams struct{}

// EditorSelectionResult reports the active selection. Empty is true when there
// is only a cursor and no selected span.
type EditorSelectionResult struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
	Empty bool   `json:"empty"`
}

// EditorWriteBufferParams writes whole-buffer content through the editor,
// respecting unsaved state. Base, when set, is the revision the write expects;
// the editor reports a conflict if the buffer has moved on.
type EditorWriteBufferParams struct {
	URI     string   `json:"uri"`
	Content string   `json:"content"`
	Base    FileBase `json:"base,omitzero"`
}

// EditorWriteBufferResult reports the buffer's new base after the write.
type EditorWriteBufferResult struct {
	Base FileBase `json:"base"`
}

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

// EditorOpenParams asks the bound editor to open files in buffers so changes
// can be inspected in place. A read-only review affordance: it mutates nothing
// and is not task-gated.
type EditorOpenParams struct {
	URIs []string `json:"uris"`
}

// EditorOpenResult acknowledges an editor.open request.
type EditorOpenResult struct{}

// EditorDiffParams asks the bound editor to render a change set's captured
// before/after snapshots in its diff/review UI. The content is carried in the
// request, so the view diffs the named proposal or applied set, never an
// undefined "current state".
type EditorDiffParams struct {
	ChangeSetID ChangeSetID      `json:"change_set_id"`
	Files       []EditorDiffFile `json:"files"`
}

// EditorDiffFile is one file's captured before/after pair for the diff view.
type EditorDiffFile struct {
	URI    string `json:"uri"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// EditorDiffResult acknowledges an editor.diff request.
type EditorDiffResult struct{}

// EditorDiagnosticsParams requests editor-fresh LSP diagnostics for a file from
// the bound editor.
type EditorDiagnosticsParams struct {
	URI string `json:"uri"`
}

// EditorDiagnosticsResult returns the editor's diagnostics for the file. Open
// is true when the file is an open buffer with attached LSP; when false the
// editor has nothing live for it and Diagnostics is empty. Severity filtering
// is applied by the daemon, so the editor returns every diagnostic it has.
type EditorDiagnosticsResult struct {
	URI         string       `json:"uri"`
	Open        bool         `json:"open"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// EditorSymbolsParams requests a file's document symbols from the bound editor.
type EditorSymbolsParams struct {
	URI string `json:"uri"`
}

// EditorSymbolsResult returns the editor's document symbols for the file. Open
// is true when the file is an open buffer with attached LSP; false means nothing
// live and Symbols is empty.
type EditorSymbolsResult struct {
	URI     string           `json:"uri"`
	Open    bool             `json:"open"`
	Symbols []DocumentSymbol `json:"symbols"`
}

// EditorDefinitionParams requests the definition of the symbol at Position.
type EditorDefinitionParams struct {
	URI      string   `json:"uri"`
	Position Position `json:"position"`
}

// EditorDefinitionResult returns the definition location(s). Open reports
// whether the queried file was a live buffer.
type EditorDefinitionResult struct {
	URI       string     `json:"uri"`
	Open      bool       `json:"open"`
	Locations []Location `json:"locations"`
}

// EditorReferencesParams requests references to the symbol at Position.
type EditorReferencesParams struct {
	URI                string   `json:"uri"`
	Position           Position `json:"position"`
	IncludeDeclaration bool     `json:"include_declaration,omitempty"`
}

// EditorReferencesResult returns the reference locations.
type EditorReferencesResult struct {
	URI       string     `json:"uri"`
	Open      bool       `json:"open"`
	Locations []Location `json:"locations"`
}

// EditorHoverParams requests hover info for the symbol at Position.
type EditorHoverParams struct {
	URI      string   `json:"uri"`
	Position Position `json:"position"`
}

// EditorHoverResult returns hover contents (markdown) and the range they cover.
type EditorHoverResult struct {
	URI      string `json:"uri"`
	Open     bool   `json:"open"`
	Contents string `json:"contents"`
	Range    *Range `json:"range,omitempty"`
}
