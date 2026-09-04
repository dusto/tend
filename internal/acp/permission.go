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

// TurnContexts resolves the context of a session's in-flight turn, so an approval
// raised for the agent's own tool is bound to the turn's lifetime rather than the
// long-lived provider connection: cancelling the turn (agent.cancel/stop, or turn
// end) then evicts the pending approval instead of leaving it to later resolve
// against a dead turn. *agent.Service satisfies it. ok is false when no turn is in
// flight, in which case the bridge falls back to the request's connection context.
type TurnContexts interface {
	TurnContext(id api.SessionID) (context.Context, bool)
}

// TurnContextFunc adapts a function to TurnContexts, so the daemon can wire a
// lazy lookup (the agent service is constructed after this handler).
type TurnContextFunc func(id api.SessionID) (context.Context, bool)

// TurnContext implements TurnContexts.
func (f TurnContextFunc) TurnContext(id api.SessionID) (context.Context, bool) { return f(id) }

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
	turns  TurnContexts // optional; nil falls back to the connection context
	emit   Emitter      // optional; nil disables artifact_written emission
}

// NewPermissionRouter wraps next so that session/request_permission is answered
// through gate (resolving the session via lookup) and everything else is
// delegated unchanged. turns, when non-nil, binds each approval to its turn's
// context so a cancelled turn evicts the pending approval; nil falls back to the
// request's connection context. emit, when non-nil, publishes an artifact_written
// record when an approved tool is a native file write (Write/Edit/MultiEdit), so a
// client can render the result inline even though the agent wrote it directly
// rather than through tend's editor tools.
func NewPermissionRouter(next rpc.Handler, gate Approver, lookup SessionLookup, turns TurnContexts, emit Emitter) *PermissionRouter {
	return &PermissionRouter{next: next, gate: gate, lookup: lookup, turns: turns, emit: emit}
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

	// Gate on the turn's context, not the long-lived provider connection: an
	// agent.cancel/stop (or the turn ending) then evicts this pending approval,
	// instead of leaving it to later resolve selected:allow against a dead turn.
	// Fall back to the connection context when no turn is in flight.
	gctx := ctx
	if r.turns != nil {
		if tctx, ok := r.turns.TurnContext(sess.ID); ok {
			gctx = tctx
		}
	}

	outcome, err := r.gate.Request(gctx, sess, api.ApprovalAgentTool, detail)
	if err != nil {
		// The turn died (ctx cancelled → agent.cancel/stop or provider gone) or the
		// session was not in a gateable state. Either way the tool cannot proceed;
		// tell the agent to abort.
		slog.Warn("acp: approval request failed; cancelling", "session", p.SessionID, "err", err)
		return cancelled(), nil
	}

	if outcome.Approved {
		// The write is approved and the agent will now perform it directly; record it
		// as an artifact (its new content + diff) so a client can render the result,
		// the same as it does for tend's own editor-tool writes.
		r.emitArtifact(sess, p.ToolCall.ToolCallID, p.ToolCall.RawInput)
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
