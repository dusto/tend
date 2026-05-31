package rpc

import (
	"encoding/json"
	"fmt"
)

// version is the JSON-RPC protocol version string.
const version = "2.0"

// message is the on-the-wire JSON-RPC 2.0 frame. A single shape represents
// requests, notifications, responses, and errors; the fields present determine
// which: a Method with an ID is a request, a Method without an ID is a
// notification, and an ID without a Method is a response (Result or Error).
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

func (m *message) isResponse() bool     { return m.Method == "" && len(m.ID) > 0 }
func (m *message) isRequest() bool      { return m.Method != "" && len(m.ID) > 0 }
func (m *message) isNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// Error is a JSON-RPC 2.0 error object. TEND-specific codes live in package api;
// the reserved internal codes used by the transport itself are defined here.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Reserved JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Request is an inbound call or notification handed to a Handler. Params is the
// raw JSON; unmarshal it into the expected type. For a notification,
// Notification is true and no response is sent.
type Request struct {
	Method       string
	Params       json.RawMessage
	Notification bool
}

// toRaw marshals v to a json.RawMessage, returning nil for a nil value.
func toRaw(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(v)
}
