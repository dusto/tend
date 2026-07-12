package sessions

import (
	"context"
	"errors"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/rpc"
)

// errLabelTooLong reports a session.rename label exceeding api.MaxSessionLabelLen.
var errLabelTooLong = errors.New("session: label too long")

// errNotRegistered reports a session.attach from a connection that has not
// registered a client identity (an empty caller cannot receive prompt delivery).
var errNotRegistered = errors.New("session: caller is not a registered client")

// Register installs the session.* methods on m. caller resolves the calling
// connection's client id (empty when the connection has not registered an
// identity) — session.list reports editor binding relative to it, and
// session.claim binds it. It is the per-connection cc.Self() hook the daemon
// already uses for agent.start and approval.respond.
func Register(m *dispatch.Mux, s *Service, caller func() api.ClientID) error {
	if err := dispatch.Handle(m, MethodList, func(_ context.Context, p api.SessionListParams) (api.SessionListResult, error) {
		return s.List(caller(), p), nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodClaim, func(_ context.Context, p api.SessionClaimParams) (api.SessionClaimResult, error) {
		res, err := s.Claim(caller(), p)
		if err != nil {
			return api.SessionClaimResult{}, toRPCError(err)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodRename, func(_ context.Context, p api.SessionRenameParams) (api.SessionRenameResult, error) {
		res, err := s.Rename(caller(), p)
		if err != nil {
			return api.SessionRenameResult{}, toRPCError(err)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodAttach, func(_ context.Context, p api.SessionAttachParams) (api.SessionAttachResult, error) {
		res, err := s.Attach(caller(), p)
		if err != nil {
			return api.SessionAttachResult{}, toRPCError(err)
		}
		return res, nil
	}); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodDetach, func(_ context.Context, p api.SessionDetachParams) (api.SessionDetachResult, error) {
		res, err := s.Detach(caller(), p)
		if err != nil {
			return api.SessionDetachResult{}, toRPCError(err)
		}
		return res, nil
	})
}

// toRPCError maps a session.* error to its JSON-RPC error. A headless/non-editor
// caller, a missing session, or an over-long label are client errors (invalid
// params); the editor binding's own "not an editor" maps the same way.
func toRPCError(err error) error {
	switch {
	case errors.Is(err, editor.ErrNoSession), errors.Is(err, editor.ErrNotEditor), errors.Is(err, editor.ErrEditorUnavailable), errors.Is(err, errLabelTooLong), errors.Is(err, errNotRegistered):
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	default:
		return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}
}
