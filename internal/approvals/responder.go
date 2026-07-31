package approvals

import (
	"context"
	"errors"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Approval responder method names.
const (
	MethodList    = "approval.list"
	MethodRespond = "approval.respond"
)

// Responder is the capability a connection must have to resolve a prompt.
// *client.Client satisfies it.
type Responder interface {
	CanRespondToPrompts() bool
}

// SelfFunc returns the calling connection's responder identity, and whether one
// is registered. It is per-connection, so the responder can capability-gate the
// caller.
type SelfFunc func() (Responder, bool)

// RegisterResponder installs approval.list and approval.respond on m. Listing is
// open to any client (read-only clients may review prompts); responding is gated
// on the caller being a prompt-capable registered client.
func RegisterResponder(m *dispatch.Mux, gate *Gate, self SelfFunc) error {
	h := &responder{gate: gate, self: self}
	if err := dispatch.Handle(m, MethodList, h.list); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodRespond, h.respond)
}

type responder struct {
	gate *Gate
	self SelfFunc
}

func (h *responder) list(_ context.Context, p api.ApprovalListParams) (api.ApprovalListResult, error) {
	pending := h.gate.List()
	out := make([]api.ApprovalSummary, 0, len(pending))
	for _, x := range pending {
		if p.SessionID != "" && x.SessionID != p.SessionID {
			continue
		}
		out = append(out, api.ApprovalSummary{
			ApprovalID: x.ID,
			SessionID:  x.SessionID,
			Kind:       x.Kind,
			Detail:     x.Detail,
		})
	}
	return api.ApprovalListResult{Approvals: out}, nil
}

func (h *responder) respond(_ context.Context, p api.ApprovalRespondParams) (api.ApprovalRespondResult, error) {
	who, ok := h.self()
	if !ok || !who.CanRespondToPrompts() {
		return api.ApprovalRespondResult{}, &rpc.Error{Code: api.ErrNotPromptCapable, Message: "approvals: client is not prompt-capable"}
	}
	err := h.gate.Resolve(p.ApprovalID, Decision{Approved: p.Approved, Reason: p.Reason})
	switch {
	case err == nil:
		return api.ApprovalRespondResult{}, nil
	case errors.Is(err, ErrUnknownApproval):
		return api.ApprovalRespondResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	default:
		return api.ApprovalRespondResult{}, &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}
}
