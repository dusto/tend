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
