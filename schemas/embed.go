// Package schemas embeds the generated JSON Schema files for the wire contract.
package schemas

import "embed"

// FS holds the generated JSON Schemas: methods/<name>.params.json and
// .result.json, events/<type>.json, errors/<name>.json, and event-envelope.json.
//
//go:embed event-envelope.json methods events errors
var FS embed.FS
