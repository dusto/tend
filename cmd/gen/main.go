// Command gen reflects the api package wire contract into JSON Schemas
// (schemas/) and a human-readable reference (docs/api.md). It is the codegen
// step for the single source of truth in api/; run it via `go generate ./...`.
// Output is deterministic so CI can fail on drift.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/invopop/jsonschema"

	"github.com/dusto/tend/api"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	reflector := &jsonschema.Reflector{ExpandedStruct: true, Anonymous: true}

	schemas := filepath.Join(root, "schemas")
	for _, sub := range []string{"methods", "events"} {
		if err := resetDir(filepath.Join(schemas, sub)); err != nil {
			return err
		}
	}

	// Event envelope.
	if err := writeSchema(reflector, filepath.Join(schemas, "event-envelope.json"), api.Event{}); err != nil {
		return err
	}
	// Method params/results.
	for _, m := range api.Methods {
		if err := writeSchema(reflector, filepath.Join(schemas, "methods", m.Name+".params.json"), m.Params); err != nil {
			return err
		}
		if m.Result != nil {
			if err := writeSchema(reflector, filepath.Join(schemas, "methods", m.Name+".result.json"), m.Result); err != nil {
				return err
			}
		}
	}
	// Event payloads.
	for _, e := range api.EventDefs {
		if e.Payload == nil {
			continue
		}
		if err := writeSchema(reflector, filepath.Join(schemas, "events", e.Type+".json"), e.Payload); err != nil {
			return err
		}
	}

	return writeDocs(filepath.Join(root, "docs", "api.md"))
}

func writeSchema(r *jsonschema.Reflector, path string, v any) error {
	b, err := json.MarshalIndent(r.Reflect(v), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func writeDocs(path string) error {
	var b strings.Builder
	b.WriteString("# TEND JSON-RPC API\n\n")
	b.WriteString("Generated from `api/` by `cmd/gen`. Do not edit by hand — run `go generate ./...`.\n\n")
	b.WriteString("Schemas live under `schemas/` (`methods/<name>.params.json` / `.result.json`, `events/<type>.json`, `event-envelope.json`).\n\n")

	b.WriteString("## Methods\n\n")
	for _, grp := range []struct {
		dir   api.Direction
		title string
	}{
		{api.PluginToDaemon, "plugin → daemon"},
		{api.DaemonToEditor, "daemon → bound editor"},
		{api.DaemonToClient, "daemon → attached client"},
	} {
		fmt.Fprintf(&b, "### %s\n\n", grp.title)
		for _, m := range api.Methods {
			if m.Direction != grp.dir {
				continue
			}
			result := "_(notification)_"
			if m.Result != nil {
				result = "`" + typeName(m.Result) + "`"
			}
			fmt.Fprintf(&b, "- **`%s`** — %s\n  - Params: `%s` · Result: %s\n", m.Name, m.Summary, typeName(m.Params), result)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Events\n\n")
	events := append([]api.EventDef(nil), api.EventDefs...)
	sort.Slice(events, func(i, j int) bool { return events[i].Type < events[j].Type })
	for _, e := range events {
		payload := "_(none)_"
		if e.Payload != nil {
			payload = "`" + typeName(e.Payload) + "`"
		}
		fmt.Fprintf(&b, "- **`%s`** (`%s` stream) — %s\n  - Payload: %s\n", e.Type, e.Scope, e.Summary, payload)
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func typeName(v any) string { return reflect.TypeOf(v).Name() }

func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// moduleRoot walks up from the working directory to the directory containing go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from working directory")
		}
		dir = parent
	}
}
