package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var errFake = errors.New("boom")

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
	// Only wired tools are advertised.
	for _, want := range []string{"read_buffer", "open_buffer", "write_buffer", "edit_buffer"} {
		if !names[want] {
			t.Errorf("tools/list missing %s; got %v", want, names)
		}
	}
}

func TestToolsCallNoCallerIsError(t *testing.T) {
	// The default server (run) has no caller wired: tools/call is a
	// tool-execution error, not a protocol error.
	resp := run(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_buffer","arguments":{"uri":"file:///x"}}}`)
	res, _ := resp[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("no-caller tools/call isError = %v, want true", res["isError"])
	}
	if _, ok := resp[0]["error"]; ok {
		t.Errorf("should be a tool-execution error (isError), not a protocol error")
	}
}

// fakeCaller records the call and returns a canned result or error.
type fakeCaller struct {
	name string
	args string
	text string
	err  error
}

func (f *fakeCaller) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	f.name, f.args = name, string(args)
	return f.text, f.err
}

func TestToolsCallRoutesToCaller(t *testing.T) {
	fc := &fakeCaller{text: "buffer contents"}
	srv := NewServer(Options{Tools: EditorTools(), Caller: fc})
	var out strings.Builder
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_buffer","arguments":{"uri":"file:///a"}}}` + "\n"
	if err := srv.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	// The caller saw the name and raw arguments.
	if fc.name != "read_buffer" || fc.args != `{"uri":"file:///a"}` {
		t.Errorf("caller got (%q, %q)", fc.name, fc.args)
	}
	// The success result carries the text and is not an error.
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &m); err != nil {
		t.Fatal(err)
	}
	res, _ := m["result"].(map[string]any)
	if res["isError"] != false {
		t.Errorf("isError = %v, want false", res["isError"])
	}
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if first["text"] != "buffer contents" {
		t.Errorf("content text = %v, want the caller's output", first["text"])
	}
}

func TestToolsCallCallerErrorIsToolError(t *testing.T) {
	fc := &fakeCaller{err: errFake}
	srv := NewServer(Options{Tools: EditorTools(), Caller: fc})
	var out strings.Builder
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_buffer","arguments":{}}}` + "\n"
	if err := srv.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(out.String())), &m)
	res, _ := m["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("caller error should surface as isError; got %v", res)
	}
	if _, ok := m["error"]; ok {
		t.Errorf("a caller error is a tool error, not a protocol error")
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

func TestKnownMethodNotificationGetsNoReply(t *testing.T) {
	// A known request method sent WITHOUT an id is a notification: per JSON-RPC
	// it must never be replied to. Regression: these leaked id:null responses
	// because reply suppression only ran in the default (unknown-method) case.
	resp := run(t,
		`{"jsonrpc":"2.0","method":"ping"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	)
	if len(resp) != 1 {
		t.Fatalf("responses = %d, want 1 (only the id'd ping)", len(resp))
	}
	if resp[0]["id"] != float64(7) {
		t.Errorf("reply id = %v, want 7", resp[0]["id"])
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
