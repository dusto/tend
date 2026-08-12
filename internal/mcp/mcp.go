// Package mcp implements a minimal Model Context Protocol (MCP) server over a
// stdio transport: newline-delimited JSON-RPC 2.0 on stdin/stdout, one message
// per line with no embedded newlines. tend advertises this server to ACP agents
// via session/new.mcpServers so an agent can call tend's editor tools — read,
// open, and (supervised) edit of the user's buffers — even when it does not use
// ACP's client fs callbacks. See docs/adr/0004.
//
// This is the protocol skeleton: initialize + tools/list advertise the tools;
// tools/call is stubbed until it is wired to the daemon (a later step). The
// server processes messages sequentially, which the stdio transport allows.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// ProtocolVersion is the MCP revision this server speaks.
const ProtocolVersion = "2025-06-18"

// maxMessageBytes bounds a single JSON-RPC line so a huge or malformed stream
// cannot exhaust memory; editor payloads (file contents) can be large.
const maxMessageBytes = 32 << 20 // 32 MiB

// JSON-RPC 2.0 error codes used here.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
)

// request is an incoming JSON-RPC message. A message with no id is a
// notification and receives no response.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (r request) isNotification() bool { return len(r.ID) == 0 }

// response is an outgoing JSON-RPC reply. Exactly one of Result/Error is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Tool is one MCP tool advertised by tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Options configures a Server.
type Options struct {
	// Name and Version identify the server in the initialize handshake.
	Name    string
	Version string
	// Tools are advertised by tools/list.
	Tools []Tool
}

// Server serves MCP over a single stdio connection.
type Server struct {
	name    string
	version string
	tools   []Tool
}

// NewServer returns a Server. Name defaults to "tend".
func NewServer(opts Options) *Server {
	name := opts.Name
	if name == "" {
		name = "tend"
	}
	return &Server{name: name, version: opts.Version, tools: opts.Tools}
}

// Serve reads newline-delimited JSON-RPC requests from in and writes responses
// to out until in is closed (the client's shutdown signal) or ctx is done. It
// returns any read error other than a clean EOF.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), maxMessageBytes)
	enc := json.NewEncoder(out) // Encoder writes one JSON value + '\n' per Encode
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		resp, reply := s.handleLine(line)
		if reply {
			if err := enc.Encode(resp); err != nil {
				return fmt.Errorf("mcp: write response: %w", err)
			}
		}
	}
	return sc.Err()
}

// handleLine parses one message and produces its response (reply=false for a
// notification or a parse error on a notification, which get no reply).
func (s *Server) handleLine(line []byte) (response, bool) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		// A parse error cannot be correlated to an id; reply with a null id.
		return errorResponse(nil, codeParse, "parse error"), true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		if req.isNotification() {
			return response{}, false
		}
		return errorResponse(req.ID, codeInvalidRequest, "invalid request"), true
	}
	return s.dispatch(req)
}

func (s *Server) dispatch(req request) (response, bool) {
	// A message with no id is a notification: per JSON-RPC it is never replied
	// to, whatever its method (so a request method sent without an id, like a
	// notification-form ping, must not leak an id:null response). Process any
	// side effects, then return no reply.
	if req.isNotification() {
		s.handleNotification(req)
		return response{}, false
	}
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, s.initializeResult()), true
	case "tools/list":
		return s.ok(req.ID, map[string]any{"tools": s.tools}), true
	case "tools/call":
		return s.ok(req.ID, s.callStub()), true
	case "ping":
		return s.ok(req.ID, struct{}{}), true
	default:
		return errorResponse(req.ID, codeMethodNotFound, "method not found: "+req.Method), true
	}
}

// handleNotification runs a notification's side effects. notifications/initialized
// and notifications/cancelled are no-ops for now; unknown notifications are
// ignored per JSON-RPC.
func (s *Server) handleNotification(_ request) {}

func (s *Server) initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		// tools is the only capability this server offers; the list is static
		// for now, so listChanged is false.
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    s.name,
			"version": s.version,
		},
	}
}

// callStub is the placeholder tools/call result until tool execution is wired to
// the daemon. It reports the failure as a tool-execution error (isError) rather
// than a protocol error, so an agent surfaces it as a failed tool run.
func (s *Server) callStub() map[string]any {
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": "tend mcp: tool execution is not yet wired to the daemon",
		}},
		"isError": true,
	}
}

func (s *Server) ok(id json.RawMessage, result any) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, msg string) response {
	if id == nil {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}
