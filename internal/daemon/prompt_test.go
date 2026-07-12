package daemon

import (
	"context"
	"sort"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// idCaller is a fake reverse caller tagged with its client id, so a test can map
// the recipients back to the clients they belong to.
type idCaller struct{ id string }

func (idCaller) Call(context.Context, string, any, any) error { return nil }
func (idCaller) Notify(context.Context, string, any) error    { return nil }

// recipientIDs returns the client ids the broadcaster would deliver a prompt for
// sessionID to, sorted for a stable assertion.
func recipientIDs(b *promptBroadcaster, sessionID api.SessionID) []string {
	var ids []string
	for _, c := range b.recipients(sessionID) {
		ids = append(ids, c.(idCaller).id)
	}
	sort.Strings(ids)
	return ids
}

func newBroadcaster(t *testing.T) (*promptBroadcaster, *session.Registry, *client.Registry) {
	t.Helper()
	sess := session.NewRegistry()
	clients := client.NewRegistry()
	return &promptBroadcaster{clients: clients, sessions: sess}, sess, clients
}

func TestRecipientsFallBackToAllClientsWhenNoneAttached(t *testing.T) {
	b, sess, clients := newBroadcaster(t)
	sess.Create("s1", "codex", "ws1", api.TaskRef{}, "/repo")
	clients.Register("ed", client.Capabilities{Role: api.RoleEditor}, idCaller{"ed"})
	clients.Register("cli", client.Capabilities{Role: api.RoleObserver}, idCaller{"cli"})

	// No client has attached: a prompt broadcasts to every connected client (the
	// pre-attach compatibility default).
	if got := recipientIDs(b, "s1"); len(got) != 2 || got[0] != "cli" || got[1] != "ed" {
		t.Errorf("recipients = %v, want [cli ed] (broadcast to all)", got)
	}
}

func TestRecipientsScopeToAttachedClients(t *testing.T) {
	b, sess, clients := newBroadcaster(t)
	s := sess.Create("s1", "codex", "ws1", api.TaskRef{}, "/repo")
	clients.Register("ed", client.Capabilities{Role: api.RoleEditor}, idCaller{"ed"})
	clients.Register("cli", client.Capabilities{Role: api.RoleObserver}, idCaller{"cli"})

	// Once a client attaches, delivery scopes to the attached set only.
	s.Attach("ed")
	if got := recipientIDs(b, "s1"); len(got) != 1 || got[0] != "ed" {
		t.Errorf("recipients = %v, want [ed] only", got)
	}

	// A second attach widens the set.
	s.Attach("cli")
	if got := recipientIDs(b, "s1"); len(got) != 2 {
		t.Errorf("recipients = %v, want both attached", got)
	}

	// Detaching the last attached client falls back to broadcasting to all.
	s.Detach("ed")
	s.Detach("cli")
	if got := recipientIDs(b, "s1"); len(got) != 2 {
		t.Errorf("recipients after full detach = %v, want fallback to all", got)
	}
}

func TestRecipientsSkipAttachedClientWithoutCaller(t *testing.T) {
	b, sess, clients := newBroadcaster(t)
	s := sess.Create("s1", "codex", "ws1", api.TaskRef{}, "/repo")
	// "ed" is attached and deliverable; "ghost" is attached but has no reverse
	// caller (registered without a connection), so it cannot receive a prompt.
	clients.Register("ed", client.Capabilities{Role: api.RoleEditor}, idCaller{"ed"})
	clients.Register("ghost", client.Capabilities{Role: api.RoleObserver}, nil)
	s.Attach("ed")
	s.Attach("ghost")

	if got := recipientIDs(b, "s1"); len(got) != 1 || got[0] != "ed" {
		t.Errorf("recipients = %v, want [ed] (ghost has no caller)", got)
	}
}

func TestRecipientsUnknownSessionBroadcasts(t *testing.T) {
	b, _, clients := newBroadcaster(t)
	clients.Register("ed", client.Capabilities{Role: api.RoleEditor}, idCaller{"ed"})
	// A prompt naming a session the registry no longer has still reaches clients
	// (no attached set to scope to) rather than vanishing.
	if got := recipientIDs(b, "gone"); len(got) != 1 || got[0] != "ed" {
		t.Errorf("recipients = %v, want [ed]", got)
	}
}

var _ rpc.Caller = idCaller{}
