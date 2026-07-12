package api

// Params/result types for the plugin->daemon LSP code-intelligence tools.
// LSP data is editor-fresh: the daemon does not run a parallel live-LSP stack,
// so these tools query the session's bound editor for open buffers. The
// optional daemon-side index is not part of milestone 0; a headless session
// (no bound editor) returns editor_unavailable.

// DiagnosticSeverity is an LSP diagnostic severity, most severe first. As a
// filter on lsp.diagnostics it is a minimum: passing "warning" returns
// warnings and errors but drops info and hint.
type DiagnosticSeverity string

// DiagnosticSeverity values (LSP's four levels).
const (
	SeverityError   DiagnosticSeverity = "error"
	SeverityWarning DiagnosticSeverity = "warning"
	SeverityInfo    DiagnosticSeverity = "info"
	SeverityHint    DiagnosticSeverity = "hint"
)

// Diagnostic is one LSP diagnostic on a file: the span it covers, its severity,
// and the message, plus the producing source/code when the server supplies them.
type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity"`
	Message  string             `json:"message"`
	Source   string             `json:"source,omitempty"`
	Code     string             `json:"code,omitempty"`
}

// LSPDiagnosticsParams requests diagnostics for a file through the session's
// bound editor. URI is the file to query; empty means the editor's current
// buffer. Severity, when set, filters to that level or more severe.
type LSPDiagnosticsParams struct {
	SessionID SessionID          `json:"session_id"`
	URI       string             `json:"uri,omitempty"`
	Severity  DiagnosticSeverity `json:"severity,omitempty"`
}

// LSPDiagnosticsResult reports a file's diagnostics. Open is true when the
// content was served from a live editor buffer; for a file the editor does not
// have open it is false and Diagnostics is empty (the editor-only milestone-0
// path does not index closed files).
type LSPDiagnosticsResult struct {
	URI         string       `json:"uri"`
	Open        bool         `json:"open"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// SymbolKind is an LSP symbol kind as a lowercase name (e.g. "function",
// "method", "struct", "variable"). The editor maps LSP's numeric SymbolKind to
// these names, so the wire contract stays provider-agnostic.
type SymbolKind string

// DocumentSymbol is one symbol in a file's outline. Range covers the whole
// symbol (e.g. a function including its body); SelectionRange covers just its
// name. ContainerName is the enclosing symbol (e.g. the type a method belongs
// to), empty at the top level. The list is flat: nesting is expressed through
// ContainerName rather than a child tree.
type DocumentSymbol struct {
	Name           string     `json:"name"`
	Kind           SymbolKind `json:"kind"`
	Detail         string     `json:"detail,omitempty"`
	ContainerName  string     `json:"container_name,omitempty"`
	Range          Range      `json:"range"`
	SelectionRange Range      `json:"selection_range"`
}

// Location is a span in a file, used by definition/references results. The URI
// may point OUTSIDE the session's worktree — a symbol defined in a dependency or
// the standard library — because navigation results are read-only location
// metadata, not a file the agent is granted to read. The query's own input file
// is still worktree-bounded.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// LSPSymbolsParams requests a file's document symbols through the session's
// bound editor. Empty URI means the editor's current buffer.
type LSPSymbolsParams struct {
	SessionID SessionID `json:"session_id"`
	URI       string    `json:"uri,omitempty"`
}

// LSPSymbolsResult reports a file's symbols. Open mirrors lsp.diagnostics: true
// when served from a live buffer, false with no symbols for a file the editor
// does not have open.
type LSPSymbolsResult struct {
	URI     string           `json:"uri"`
	Open    bool             `json:"open"`
	Symbols []DocumentSymbol `json:"symbols"`
}

// LSPDefinitionParams requests the definition location(s) of the symbol at
// Position in a file. Empty URI means the editor's current buffer.
type LSPDefinitionParams struct {
	SessionID SessionID `json:"session_id"`
	URI       string    `json:"uri,omitempty"`
	Position  Position  `json:"position"`
}

// LSPDefinitionResult reports the definition location(s). Locations is empty
// when the editor has nothing (file not open, or no definition at the position).
// URI/Open describe the queried file, not the results (whose URIs are their own).
type LSPDefinitionResult struct {
	URI       string     `json:"uri"`
	Open      bool       `json:"open"`
	Locations []Location `json:"locations"`
}

// LSPReferencesParams requests the references to the symbol at Position.
// IncludeDeclaration adds the declaration itself to the results. Empty URI means
// the editor's current buffer.
type LSPReferencesParams struct {
	SessionID          SessionID `json:"session_id"`
	URI                string    `json:"uri,omitempty"`
	Position           Position  `json:"position"`
	IncludeDeclaration bool      `json:"include_declaration,omitempty"`
}

// LSPReferencesResult reports the reference locations. URI/Open describe the
// queried file.
type LSPReferencesResult struct {
	URI       string     `json:"uri"`
	Open      bool       `json:"open"`
	Locations []Location `json:"locations"`
}

// LSPHoverParams requests hover info for the symbol at Position. Empty URI means
// the editor's current buffer.
type LSPHoverParams struct {
	SessionID SessionID `json:"session_id"`
	URI       string    `json:"uri,omitempty"`
	Position  Position  `json:"position"`
}

// LSPHoverResult reports hover contents (markdown) and the range they describe.
// Contents is empty when the editor has nothing at the position. Range is nil
// when the server did not attach one.
type LSPHoverResult struct {
	URI      string `json:"uri"`
	Open     bool   `json:"open"`
	Contents string `json:"contents"`
	Range    *Range `json:"range,omitempty"`
}
