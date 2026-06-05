package daemon

import (
	"context"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/approvals"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/rpc"
)

// raiseTimeout bounds a single prompt.raise delivery to one client.
const raiseTimeout = 5 * time.Second

// promptBroadcaster raises a requested approval to every connected client via the
// daemon->client prompt.raise notification. Read-only clients receive it too (to
// review); only prompt-capable clients can resolve it via approval.respond.
type promptBroadcaster struct {
	clients *client.Registry
}

// RaiseApproval implements approvals.Prompter. It composes the gate-owned
// envelope (id, kind, expiry) with the decision detail and notifies each client
// in its own goroutine, so a slow client cannot stall the gating turn.
func (b *promptBroadcaster) RaiseApproval(p approvals.Pending) {
	params := api.PromptRaiseParams{
		SessionID:  p.SessionID,
		Kind:       "approval",
		ApprovalID: p.ID,
		Prompt:     promptText(p.Kind),
		Detail:     p.Detail,
		ExpiresAt:  p.ExpiresAt,
	}
	for _, c := range b.clients.List() {
		if c.Caller == nil {
			continue
		}
		go func(caller rpc.Caller) {
			ctx, cancel := context.WithTimeout(context.Background(), raiseTimeout)
			defer cancel()
			_ = caller.Notify(ctx, "prompt.raise", params)
		}(c.Caller)
	}
}

// promptText is the short human-facing line for an approval of the given kind;
// the specifics travel in the structured detail.
func promptText(kind string) string {
	switch kind {
	case api.ApprovalFileEdit:
		return "Apply proposed file changes?"
	case api.ApprovalPaneRun:
		return "Run command in pane?"
	case api.ApprovalCodeAction:
		return "Apply code action?"
	default:
		return "Approve " + kind + "?"
	}
}
