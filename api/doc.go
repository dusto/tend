// Package api is the single source of truth for the TEND JSON-RPC wire contract:
// one type per method's params and result, and one per event payload, grouped by
// direction (plugin->daemon, daemon->bound-editor, daemon->attached-client).
// Codegen reflects these types into schemas/ (JSON Schema) and docs/api.md.
package api
