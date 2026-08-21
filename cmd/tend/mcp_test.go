package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
)

// These paths fail before the daemon connection is used, so a nil conn is fine.
func TestDaemonCallerValidation(t *testing.T) {
	tests := []struct {
		name    string
		caller  *daemonCaller
		tool    string
		args    string
		wantErr string
	}{
		{"unknown tool", &daemonCaller{session: "s1"}, "frobnicate", `{}`, "unknown tool"},
		{"read missing uri", &daemonCaller{session: "s1"}, "read_buffer", `{}`, "uri is required"},
		{"read no session", &daemonCaller{session: ""}, "read_buffer", `{"uri":"file:///a"}`, "no session"},
		{"read bad json", &daemonCaller{session: "s1"}, "read_buffer", `{bad`, "invalid arguments"},

		{"open missing uri", &daemonCaller{session: "s1"}, "open_buffer", `{}`, "uri is required"},
		{"open no session", &daemonCaller{session: ""}, "open_buffer", `{"uri":"file:///a"}`, "no session"},
		{"open bad json", &daemonCaller{session: "s1"}, "open_buffer", `{bad`, "invalid arguments"},

		{"write missing uri", &daemonCaller{session: "s1"}, "write_buffer", `{"new_text":"x"}`, "uri is required"},
		{"write missing new_text", &daemonCaller{session: "s1"}, "write_buffer", `{"uri":"file:///a"}`, "new_text is required"},
		{"write no session", &daemonCaller{session: ""}, "write_buffer", `{"uri":"file:///a","new_text":"x"}`, "no session"},
		{"write bad json", &daemonCaller{session: "s1"}, "write_buffer", `{bad`, "invalid arguments"},

		{"edit missing uri", &daemonCaller{session: "s1"}, "edit_buffer", `{"edits":[{"start_line":0,"start_column":0,"end_line":0,"end_column":0,"new_text":"x"}]}`, "uri is required"},
		{"edit no edits", &daemonCaller{session: "s1"}, "edit_buffer", `{"uri":"file:///a","edits":[]}`, "edits is required"},
		{"edit missing range field", &daemonCaller{session: "s1"}, "edit_buffer", `{"uri":"file:///a","edits":[{"new_text":"hi"}]}`, "edits[0]:"},
		{"edit missing new_text", &daemonCaller{session: "s1"}, "edit_buffer", `{"uri":"file:///a","edits":[{"start_line":0,"start_column":0,"end_line":0,"end_column":0}]}`, "edits[0]:"},
		{"edit no session", &daemonCaller{session: ""}, "edit_buffer", `{"uri":"file:///a","edits":[{"start_line":0,"start_column":0,"end_line":0,"end_column":0,"new_text":"x"}]}`, "no session"},
		{"edit bad json", &daemonCaller{session: "s1"}, "edit_buffer", `{bad`, "invalid arguments"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.caller.Call(context.Background(), tc.tool, json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// A directly-bound session id needs no resolution; a bridge with neither a
// session nor a token has nothing to serve. (Token resolution itself dials the
// daemon and is covered by the daemon-side ResolveBridge tests.)
func TestSessionIDWithoutResolve(t *testing.T) {
	sid, err := (&daemonCaller{session: "s1"}).sessionID(context.Background())
	if err != nil || sid != "s1" {
		t.Errorf("sessionID = %q, %v; want s1, nil", sid, err)
	}
	if _, err := (&daemonCaller{}).sessionID(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "no session") {
		t.Errorf("err = %v, want containing 'no session'", err)
	}
}

// An explicit empty new_text is a legitimate whole-file clear and must pass
// validation — only an omitted new_text is rejected.
func TestParseWriteArgsAllowsEmptyNewText(t *testing.T) {
	uri, newText, err := parseWriteArgs(json.RawMessage(`{"uri":"file:///a","new_text":""}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uri != "file:///a" || newText != "" {
		t.Errorf("got uri=%q newText=%q, want file:///a and empty", uri, newText)
	}
}

// A well-formed edit maps to api.TextEdit; an explicit empty new_text (a pure
// range deletion) is allowed, while a zero-valued position is honored as given
// rather than treated as absent.
func TestParseEditArgsMapsEdits(t *testing.T) {
	uri, edits, err := parseEditArgs(json.RawMessage(
		`{"uri":"file:///a","edits":[{"start_line":1,"start_column":2,"end_line":3,"end_column":4,"new_text":""}]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uri != "file:///a" || len(edits) != 1 {
		t.Fatalf("got uri=%q edits=%d, want file:///a and 1 edit", uri, len(edits))
	}
	want := api.TextEdit{
		Range: api.Range{
			Start: api.Position{Line: 1, ByteCol: 2},
			End:   api.Position{Line: 3, ByteCol: 4},
		},
		NewText: "",
	}
	if edits[0] != want {
		t.Errorf("edit = %+v, want %+v", edits[0], want)
	}
}

// A mutation the user did not approve is reported as plain output, not a tool
// error, so the agent learns the outcome instead of treating it as a failure.
func TestMutationSummary(t *testing.T) {
	tests := []struct {
		name string
		res  api.FileMutationResult
		want string
	}{
		{"applied", api.FileMutationResult{Applied: true}, "applied"},
		{"denied with reason", api.FileMutationResult{Applied: false, Reason: "denied"}, "not applied: denied"},
		{"denied no reason", api.FileMutationResult{Applied: false}, "not applied (the user did not approve the change)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mutationSummary(tc.res); got != tc.want {
				t.Errorf("mutationSummary = %q, want %q", got, tc.want)
			}
		})
	}
}
