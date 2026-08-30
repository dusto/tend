package acp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// PermissionMethod is the ACP request an agent issues when one of its own tools
// (Write/Edit/Bash/…) needs approval. It is a JSON-RPC *request* the agent then
// awaits; the client must answer it with a selected option or a cancellation.
const PermissionMethod = "session/request_permission"

// SessionLookup resolves a session id to its live session. *session.Registry
// satisfies it.
type SessionLookup interface {
	Get(id api.SessionID) (*session.Session, bool)
}

// Approver gates a provider-native tool call, blocking until a prompt-capable
// client resolves it. *approvals.Gate satisfies it.
type Approver interface {
	Request(ctx context.Context, sess *session.Session, kind string, detail json.RawMessage) (approvals.Outcome, error)
}

// PermissionRouter is the inbound ACP handler installed on a provider process. It
// bridges the agent's own permission requests into tend's approval gate: a
// session/request_permission request is raised as an approval on the workspace
// stream (where prompt-capable clients answer it), and its resolution is mapped
// back to the ACP option the agent expects. Every other inbound message —
// notifications (session/update) and any other request — is delegated to next
// (the Normalizer), so this is a strict superset of the previous behaviour.
type PermissionRouter struct {
	next   rpc.Handler
	gate   Approver
	lookup SessionLookup
}

// NewPermissionRouter wraps next so that session/request_permission is answered
// through gate (resolving the session via lookup) and everything else is
// delegated unchanged.
func NewPermissionRouter(next rpc.Handler, gate Approver, lookup SessionLookup) *PermissionRouter {
	return &PermissionRouter{next: next, gate: gate, lookup: lookup}
}

// Handle implements rpc.Handler.
func (r *PermissionRouter) Handle(ctx context.Context, req *rpc.Request) (any, error) {
	if req.Notification || req.Method != PermissionMethod {
		return r.next.Handle(ctx, req)
	}
	return r.handlePermission(ctx, req)
}

// permissionRequest is the subset of the ACP session/request_permission params we
// read: which session, the tool call (id, title, kind, input), and the options
// the agent offers us to answer with.
type permissionRequest struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string          `json:"toolCallId"`
		Title      string          `json:"title"`
		Kind       string          `json:"kind"`
		RawInput   json.RawMessage `json:"rawInput"`
	} `json:"toolCall"`
	Options []permissionOption `json:"options"`
}

// permissionOption is one choice the agent offers. Kind is one of allow_always,
// allow_once, reject_once, reject_always; OptionID is the token we echo back.
type permissionOption struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	OptionID string `json:"optionId"`
}

// permissionResponse is the ACP result: either a selected option or a
// cancellation. The agent treats a reject option as a denial and a cancellation
// as an abort; both stop the tool.
type permissionResponse struct {
	Outcome permissionOutcome `json:"outcome"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"` // "selected" | "cancelled"
	OptionID string `json:"optionId,omitempty"`
}

func cancelled() permissionResponse {
	return permissionResponse{Outcome: permissionOutcome{Outcome: "cancelled"}}
}

func selected(optionID string) permissionResponse {
	return permissionResponse{Outcome: permissionOutcome{Outcome: "selected", OptionID: optionID}}
}

func (r *PermissionRouter) handlePermission(ctx context.Context, req *rpc.Request) (any, error) {
	var p permissionRequest
	if err := json.Unmarshal(req.Params, &p); err != nil || p.SessionID == "" {
		// A malformed request cannot be gated; abort the tool rather than allow an
		// unsupervised action.
		slog.Warn("acp: unparseable request_permission; cancelling", "err", err)
		return cancelled(), nil
	}

	sess, ok := r.lookup.Get(api.SessionID(p.SessionID))
	if !ok {
		// No live session to attribute the approval to — cannot supervise it, so
		// deny by cancelling rather than silently allowing.
		slog.Warn("acp: request_permission for unknown session; cancelling", "session", p.SessionID)
		return cancelled(), nil
	}

	detail, _ := json.Marshal(api.ApprovalDetail{
		Kind: api.ApprovalAgentTool,
		AgentTool: &api.AgentToolApproval{
			ToolCallID: p.ToolCall.ToolCallID,
			Title:      p.ToolCall.Title,
			ToolKind:   p.ToolCall.Kind,
			RawInput:   p.ToolCall.RawInput,
		},
	})

	outcome, err := r.gate.Request(ctx, sess, api.ApprovalAgentTool, detail)
	if err != nil {
		// The turn died (ctx cancelled → provider process gone) or the session was
		// not in a gateable state. Either way the tool cannot proceed; tell the
		// agent to abort.
		slog.Warn("acp: approval request failed; cancelling", "session", p.SessionID, "err", err)
		return cancelled(), nil
	}

	if outcome.Approved {
		if id, ok := pickOption(p.Options, "allow"); ok {
			return selected(id), nil
		}
		// The agent offered no allow option we recognise; safest is to abort.
		slog.Warn("acp: approved but no allow option offered; cancelling", "session", p.SessionID)
		return cancelled(), nil
	}
	if id, ok := pickOption(p.Options, "reject"); ok {
		return selected(id), nil
	}
	// No explicit reject option: a cancellation denies the tool just the same.
	return cancelled(), nil
}

// pickOption chooses the option to answer with for a decision. prefix is "allow"
// or "reject"; it prefers the "_once" variant (approve/deny just this call, not a
// standing mode change) and otherwise takes the first option of that class.
func pickOption(options []permissionOption, prefix string) (string, bool) {
	once := prefix + "_once"
	var fallback string
	haveFallback := false
	for _, o := range options {
		if o.Kind == once {
			return o.OptionID, true
		}
		if !haveFallback && strings.HasPrefix(o.Kind, prefix) {
			fallback, haveFallback = o.OptionID, true
		}
	}
	return fallback, haveFallback
}
