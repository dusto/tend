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

		{"write missing uri", &daemonCaller{session: "s1"}, "write_buffer", `{"new_text":"x"}`, "uri is required"},
		{"write no session", &daemonCaller{session: ""}, "write_buffer", `{"uri":"file:///a","new_text":"x"}`, "no session"},
		{"write bad json", &daemonCaller{session: "s1"}, "write_buffer", `{bad`, "invalid arguments"},

		{"edit missing uri", &daemonCaller{session: "s1"}, "edit_buffer", `{"edits":[{"start_line":0,"start_column":0,"end_line":0,"end_column":0,"new_text":"x"}]}`, "uri is required"},
		{"edit no edits", &daemonCaller{session: "s1"}, "edit_buffer", `{"uri":"file:///a","edits":[]}`, "edits is required"},
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
