package api

// TEND-specific JSON-RPC error codes. JSON-RPC reserves -32768..-32000; these
// application codes sit in the positive range and carry typed Data.
const (
	// ErrCursorCompacted: the requested stream cursor predates the exact-replay
	// retention window. Data is CursorCompactedData (resume at the boundary).
	ErrCursorCompacted = 1001
	// ErrConflict: a file mutation's cited base no longer matches (disk hash or
	// buffer changedtick changed since the proposal). Data is ConflictData.
	ErrConflict = 1002
	// ErrEditorUnavailable: an editor-local capability was requested but no
	// editor holds the session's binding.
	ErrEditorUnavailable = 1003
	// ErrNoWorkspaceMutation: a mutating operation was attempted outside git
	// (only an ephemeral read-only workspace is available).
	ErrNoWorkspaceMutation = 1004
	// ErrNoActiveWorkspace: workspace.current was called before any workspace
	// was opened on the connection.
	ErrNoActiveWorkspace = 1005
)

// CursorCompactedData accompanies ErrCursorCompacted: the client should resume
// the stream from BoundarySeq (the summary record), rendering older turns as
// summaries.
type CursorCompactedData struct {
	StreamID    StreamID `json:"stream_id"`
	BoundarySeq uint64   `json:"boundary_seq"`
}

// ConflictData accompanies ErrConflict.
type ConflictData struct {
	URI string `json:"uri"`
}

// ErrorDef declares a TEND JSON-RPC error for the generated contract. Data
// holds a zero value of the error's Data struct (nil for errors with no Data).
type ErrorDef struct {
	Code    int
	Name    string
	Data    any
	Summary string
}

// ErrorDefs is the catalog of TEND-specific JSON-RPC errors.
var ErrorDefs = []ErrorDef{
	{Code: ErrCursorCompacted, Name: "cursor_compacted", Data: CursorCompactedData{}, Summary: "Stream cursor predates the exact-replay retention window; resume at the summary boundary."},
	{Code: ErrConflict, Name: "conflict", Data: ConflictData{}, Summary: "A file mutation's cited base no longer matches (disk hash or buffer changedtick changed)."},
	{Code: ErrEditorUnavailable, Name: "editor_unavailable", Data: nil, Summary: "An editor-local capability was requested but no editor holds the session's binding."},
	{Code: ErrNoWorkspaceMutation, Name: "no_workspace_mutation", Data: nil, Summary: "A mutating operation was attempted outside git (only an ephemeral read-only workspace is available)."},
	{Code: ErrNoActiveWorkspace, Name: "no_active_workspace", Data: nil, Summary: "workspace.current was called before any workspace was opened on the connection."},
}
