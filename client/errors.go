package client

import (
	"encoding/json"
	"errors"

	"github.com/dusto/tend/internal/rpc"
)

// Error is a JSON-RPC error returned by the daemon, exposed at the client
// boundary so external callers can branch on Code without importing tend's
// internal rpc package. Domain codes and their typed Data live in the api
// package (api.ErrCursorCompacted, api.ErrConflict, …); the standard JSON-RPC
// codes are the negative values (see IsMethodNotFound and friends).
type Error struct {
	Code    int
	Message string
	// Data is the raw typed payload for codes that carry one (e.g.
	// api.CursorCompactedData for api.ErrCursorCompacted); nil otherwise.
	Data json.RawMessage
}

// Error implements error.
func (e *Error) Error() string { return e.Message }

// AsError reports whether err is (or wraps) a daemon Error, returning it.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsCode reports whether err is a daemon Error with the given code — the common
// branch, e.g. client.IsCode(err, api.ErrCursorCompacted).
func IsCode(err error, code int) bool {
	e, ok := AsError(err)
	return ok && e.Code == code
}

// asClientError converts a transport rpc.Error into the exported Error, leaving
// any other error untouched. Call routes call errors through this so external
// callers never see the internal type.
func asClientError(err error) error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return &Error{Code: re.Code, Message: re.Message, Data: re.Data}
	}
	return err
}
