package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// run feeds newline-delimited request lines through a Server and returns the
// decoded response objects (notifications produce no line, so they are absent).
func run(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	srv := NewServer(Options{Version: "v1.2.3", Tools: EditorTools()})
	var out strings.Builder
	if err := srv.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var got []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		got = append(got, m)
	}
	return got
}

func TestInitialize(t *testing.T) {
	resp := run(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`)
	if len(resp) != 1 {
		t.Fatalf("responses = %d, want 1", len(resp))
	}
	res, _ := resp[0]["result"].(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %s", res["protocolVersion"], ProtocolVersion)
	}
	caps, _ := res["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities missing tools: %v", caps)
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "tend" || info["version"] != "v1.2.3" {
		t.Errorf("serverInfo = %v, want tend/v1.2.3", info)
	}
}

func TestToolsList(t *testing.T) {
	resp := run(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	res, _ := resp[0]["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	names := map[string]bool{}
	for _, tv := range tools {
		tm, _ := tv.(map[string]any)
		names[tm["name"].(string)] = true
		if tm["inputSchema"] == nil {
			t.Errorf("tool %v has no inputSchema", tm["name"])
		}
	}
	for _, want := range []string{"read_buffer", "open_buffer", "edit_buffer"} {
		if !names[want] {
			t.Errorf("tools/list missing %q; got %v", want, names)
		}
	}
}

func TestToolsCallStubIsError(t *testing.T) {
	resp := run(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_buffer","arguments":{"uri":"file:///x"}}}`)
	res, _ := resp[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("stub tools/call isError = %v, want true", res["isError"])
	}
	if _, ok := resp[0]["error"]; ok {
		t.Errorf("stub should be a tool-execution error (isError), not a protocol error")
	}
}

func TestNotificationGetsNoReply(t *testing.T) {
	// A notification (no id) is not replied to; a following request still is.
	resp := run(t,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":9,"method":"ping"}`,
	)
	if len(resp) != 1 {
		t.Fatalf("responses = %d, want 1 (only the ping)", len(resp))
	}
	if resp[0]["id"] != float64(9) {
		t.Errorf("reply id = %v, want 9", resp[0]["id"])
	}
}

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	resp := run(t, `{"jsonrpc":"2.0","id":4,"method":"does/not/exist"}`)
	errObj, _ := resp[0]["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(codeMethodNotFound) {
		t.Errorf("error = %v, want method-not-found (%d)", errObj, codeMethodNotFound)
	}
}

func TestMalformedLineIsParseError(t *testing.T) {
	resp := run(t, `{not json`)
	errObj, _ := resp[0]["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(codeParse) {
		t.Errorf("error = %v, want parse error (%d)", errObj, codeParse)
	}
	if resp[0]["id"] != nil {
		t.Errorf("parse-error id = %v, want null", resp[0]["id"])
	}
}
