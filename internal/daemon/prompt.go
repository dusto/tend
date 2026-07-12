package daemon

import (
	"context"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// raiseTimeout bounds a single prompt.raise delivery to one client.
const raiseTimeout = 5 * time.Second

// promptBroadcaster raises a requested approval to a session's attached clients
// via the daemon->client prompt.raise notification. When no client has attached
// to the session it falls back to every connected client (the pre-attach
// default), so a single-editor setup is unchanged. Read-only clients receive it
// too (to review); only prompt-capable clients can resolve it via
// approval.respond.
type promptBroadcaster struct {
	clients  *client.Registry
	sessions *session.Registry
}

// RaiseApproval implements approvals.Prompter. It composes the gate-owned
// envelope (id, kind, expiry) with the decision detail and notifies each
// recipient in its own goroutine, so a slow client cannot stall the gating turn.
func (b *promptBroadcaster) RaiseApproval(p approvals.Pending) {
	params := api.PromptRaiseParams{
		SessionID:  p.SessionID,
		Kind:       "approval",
		ApprovalID: p.ID,
		Prompt:     promptText(p.Kind),
		Detail:     p.Detail,
		ExpiresAt:  p.ExpiresAt,
	}
	for _, caller := range b.recipients(p.SessionID) {
		go func(caller rpc.Caller) {
			ctx, cancel := context.WithTimeout(context.Background(), raiseTimeout)
			defer cancel()
			_ = caller.Notify(ctx, "prompt.raise", params)
		}(caller)
	}
}

// recipients returns the reverse callers a prompt for sessionID should reach:
// the session's attached clients, or — when none are attached — every connected
// client (the pre-attach compatibility default). Clients without a reverse
// caller (registered without a connection) are skipped.
func (b *promptBroadcaster) recipients(sessionID api.SessionID) []rpc.Caller {
	var attached []api.ClientID
	if sess, ok := b.sessions.Get(sessionID); ok {
		attached = sess.AttachedClients()
	}
	out := make([]rpc.Caller, 0, len(attached))
	if len(attached) == 0 {
		for _, c := range b.clients.List() {
			if c.Caller != nil {
				out = append(out, c.Caller)
			}
		}
		return out
	}
	for _, id := range attached {
		if c, ok := b.clients.Get(id); ok && c.Caller != nil {
			out = append(out, c.Caller)
		}
	}
	return out
}

// promptText is the short human-facing line for an approval of the given kind;
// the specifics travel in the structured detail.
func promptText(kind string) string {
	switch kind {
	case api.ApprovalFileEdit:
		return "Apply proposed file changes?"
	case api.ApprovalPaneOpen:
		return "Open a terminal pane?"
	case api.ApprovalPaneRun:
		return "Run command in pane?"
	case api.ApprovalCodeAction:
		return "Apply code action?"
	default:
		return "Approve " + kind + "?"
	}
}
