package editor

import (
	"errors"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/client"
	"github.com/dusto/tend/internal/session"
)

// Errors returned by the Binder.
var (
	// ErrNoSession reports that no session has the given id.
	ErrNoSession = errors.New("editor: unknown session")
	// ErrNotEditor reports that the client is not connected or cannot serve
	// editor-local operations.
	ErrNotEditor = errors.New("editor: client is not an editor-capable connected client")
	// ErrEditorUnavailable reports that a session has no editor-binding owner, so
	// editor-local calls cannot be served. It maps to api.ErrEditorUnavailable.
	ErrEditorUnavailable = errors.New("editor: no editor bound to session")
)

// Binder owns editor-binding decisions across sessions: identity-checked
// auto-bind on attach, deliberate claim (takeover), and release to headless on
// disconnect. The binding state itself lives on each session; the Binder applies
// the identity and capability rules over the session and client registries. It
// is safe for concurrent use (its state is the underlying registries').
type Binder struct {
	sessions *session.Registry
	clients  *client.Registry
}

// NewBinder returns a Binder over the session and client registries.
func NewBinder(sessions *session.Registry, clients *client.Registry) *Binder {
	return &Binder{sessions: sessions, clients: clients}
}

// Attach auto-binds an editor-capable client to a session, but only if the
// client's identity matches the session's expected editor and the session is
// currently headless. It reports whether it bound; a non-matching editor leaves
// the session headless ("needs editor") rather than capturing a session whose
// editor-local effects belong to a different instance.
func (b *Binder) Attach(sessionID api.SessionID, clientID api.ClientID) (bool, error) {
	sess, err := b.editorSession(sessionID, clientID)
	if err != nil {
		return false, err
	}
	return sess.AutoBindOwner(clientID), nil
}

// Claim moves the editor binding for a session to clientID, transferring it from
// any existing owner and recording clientID as the session's expected editor.
// This is the deliberate-takeover path for resuming a session in a new or
// different editor. clientID must be an editor-capable connected client.
func (b *Binder) Claim(sessionID api.SessionID, clientID api.ClientID) error {
	sess, err := b.editorSession(sessionID, clientID)
	if err != nil {
		return err
	}
	sess.BindOwner(clientID)
	return nil
}

// ReleaseClient releases every binding owned by clientID, leaving those sessions
// headless. It is the disconnect hook: a session keeps running headless until a
// matching editor reattaches or a user claims it.
func (b *Binder) ReleaseClient(clientID api.ClientID) {
	for _, sess := range b.sessions.List() {
		sess.ReleaseOwner(clientID)
	}
}

// Owner returns the editor-binding owner for a session, or ErrEditorUnavailable
// if the session is headless. ErrNoSession is returned for an unknown session.
func (b *Binder) Owner(sessionID api.SessionID) (api.ClientID, error) {
	sess, ok := b.sessions.Get(sessionID)
	if !ok {
		return "", ErrNoSession
	}
	owner, bound := sess.Owner()
	if !bound {
		return "", ErrEditorUnavailable
	}
	return owner, nil
}

// editorSession resolves the session and verifies the client is a connected,
// editor-capable client before a bind decision.
func (b *Binder) editorSession(sessionID api.SessionID, clientID api.ClientID) (*session.Session, error) {
	sess, ok := b.sessions.Get(sessionID)
	if !ok {
		return nil, ErrNoSession
	}
	cl, ok := b.clients.Get(clientID)
	if !ok || !cl.IsEditor() {
		return nil, ErrNotEditor
	}
	return sess, nil
}
