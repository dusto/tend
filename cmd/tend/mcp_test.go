package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
		{"missing uri", &daemonCaller{session: "s1"}, "read_buffer", `{}`, "uri is required"},
		{"no session", &daemonCaller{session: ""}, "read_buffer", `{"uri":"file:///a"}`, "no session"},
		{"bad json", &daemonCaller{session: "s1"}, "read_buffer", `{bad`, "invalid arguments"},
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
