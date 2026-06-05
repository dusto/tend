// Package approvals implements the approval gate and pending-approval state. A
// mutating tool calls Request before it executes; the gate marks the owning
// session waiting_approval, raises an approval_requested event, and blocks the
// turn until the approval is resolved. Resolve (driven by a prompt-capable
// client) unblocks the turn with the decision, raises approval_resolved, and
// returns the session to running. The decision context carried in an approval
// payload is supplied by the caller, so the gate is agnostic to the operation
// kind (file edit, pane run, code action).
package approvals

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/session"
)

// ErrUnknownApproval is returned by Resolve when no pending approval has the id
// (it was never created, already resolved, or its turn was cancelled).
var ErrUnknownApproval = errors.New("approvals: unknown or already-resolved approval")

// Decision resolves a pending approval.
type Decision struct {
	Approved bool
	Reason   string
}

// Outcome is what Request returns once an approval is resolved: the decision the
// turn should act on.
type Outcome struct {
	Approved bool
	Reason   string
}

// Emitter publishes events. *events.Store satisfies it.
type Emitter interface {
	Publish(api.Event) (api.Event, error)
}

// Pending is a snapshot of an approval still awaiting a decision. ExpiresAt is
// the gate's deadline (zero when no TTL is configured); enforcing it is layered
// on separately.
type Pending struct {
	ID        api.ApprovalID
	SessionID api.SessionID
	Kind      string
	Detail    json.RawMessage
	Created   time.Time
	ExpiresAt time.Time
}

type pending struct {
	id        api.ApprovalID
	sess      *session.Session
	kind      string
	detail    json.RawMessage
	created   time.Time
	expiresAt time.Time
	resolve   chan Outcome
}

// Gate owns the set of pending approvals and the block/resolve handshake. It is
// safe for concurrent use.
type Gate struct {
	emit  Emitter
	newID func() api.ApprovalID
	now   func() time.Time
	ttl   time.Duration

	mu      sync.Mutex
	pending map[api.ApprovalID]*pending
}

// Options configures a Gate. The zero value is valid: crypto/rand ids, time.Now,
// and no expiry.
type Options struct {
	// NewID returns a fresh approval id; nil uses a random hex generator.
	NewID func() api.ApprovalID
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
	// TTL stamps each approval's ExpiresAt as created+TTL. Zero leaves ExpiresAt
	// zero (no expiry). Enforcing the deadline is layered on separately.
	TTL time.Duration
}

// NewGate returns a Gate that raises events through emit (which may be nil to
// skip emission).
func NewGate(emit Emitter, opts Options) *Gate {
	newID := opts.NewID
	if newID == nil {
		newID = randomID
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Gate{emit: emit, newID: newID, now: now, ttl: opts.TTL, pending: make(map[api.ApprovalID]*pending)}
}

// Request gates a mutating action on sess: it marks the session waiting_approval
// with a fresh approval id, raises approval_requested with the supplied decision
// context, and blocks until the approval is resolved or ctx is done. The session
// must be running (a turn is in flight); Request returns the session-state error
// otherwise. On ctx cancellation the pending approval is dropped and ctx.Err is
// returned; the caller owns the session's status in that case.
func (g *Gate) Request(ctx context.Context, sess *session.Session, kind string, detail json.RawMessage) (Outcome, error) {
	id := g.newID()
	if err := sess.SetStatus(api.StatusWaitingApproval, &session.Pending{Kind: api.PendingApproval, ID: string(id)}); err != nil {
		return Outcome{}, err
	}
	created := g.now()
	var expiresAt time.Time
	if g.ttl > 0 {
		expiresAt = created.Add(g.ttl)
	}
	p := &pending{
		id:        id,
		sess:      sess,
		kind:      kind,
		detail:    detail,
		created:   created,
		expiresAt: expiresAt,
		resolve:   make(chan Outcome, 1),
	}
	g.mu.Lock()
	g.pending[id] = p
	g.mu.Unlock()

	g.emit2(sess.Stream, "approval_requested", api.ApprovalRequested{
		SessionID:  sess.ID,
		ApprovalID: id,
		Kind:       kind,
	})

	select {
	case <-ctx.Done():
		// If we still own the pending, the turn was cancelled before a decision;
		// otherwise a resolve raced in and we take its outcome.
		if g.claim(id) != nil {
			return Outcome{}, ctx.Err()
		}
		return <-p.resolve, nil
	case out := <-p.resolve:
		return out, nil
	}
}

// Resolve settles the pending approval id with d: it returns the session to
// running, raises approval_resolved, and unblocks the waiting Request. It returns
// ErrUnknownApproval if no such pending approval exists.
//
// If the session has already left waiting_approval (for example it was ended or
// cancelled through another path), the approval is stale: Resolve does not apply
// the decision. It unblocks the waiting Request with a denial so the gated tool
// cannot proceed, raises no approval_resolved, and returns an error.
func (g *Gate) Resolve(id api.ApprovalID, d Decision) error {
	p := g.claim(id)
	if p == nil {
		return ErrUnknownApproval
	}
	// A resolved approval (approved or denied) returns the turn to running:
	// either the action executes or the agent is told it was rejected.
	if err := p.sess.SetStatus(api.StatusRunning, nil); err != nil {
		p.resolve <- Outcome{Approved: false, Reason: "session no longer awaiting approval"}
		return fmt.Errorf("approvals: resolve %s: %w", id, err)
	}

	out := Outcome(d)
	g.emit2(p.sess.Stream, "approval_resolved", api.ApprovalResolved{
		SessionID:  p.sess.ID,
		ApprovalID: id,
		Approved:   d.Approved,
		Reason:     d.Reason,
	})
	p.resolve <- out
	return nil
}

// List returns a snapshot of the approvals still awaiting a decision, in
// unspecified order.
func (g *Gate) List() []Pending {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Pending, 0, len(g.pending))
	for _, p := range g.pending {
		out = append(out, p.snapshot())
	}
	return out
}

// Get returns a snapshot of one pending approval.
func (g *Gate) Get(id api.ApprovalID) (Pending, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pending[id]
	if !ok {
		return Pending{}, false
	}
	return p.snapshot(), true
}

// claim removes and returns the pending approval id, or nil if it is gone. It is
// the single authority for ownership: only the caller that claims an id may
// resolve or cancel it, so a decision and a cancellation cannot both fire.
func (g *Gate) claim(id api.ApprovalID) *pending {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.pending[id]
	if p != nil {
		delete(g.pending, id)
	}
	return p
}

func (g *Gate) emit2(stream api.StreamID, typ string, payload any) {
	if g.emit == nil {
		return
	}
	raw, _ := json.Marshal(payload)
	_, _ = g.emit.Publish(api.Event{
		StreamID: stream,
		Scope:    api.ScopeSession,
		Type:     typ,
		Payload:  raw,
	})
}

func (p *pending) snapshot() Pending {
	return Pending{
		ID:        p.id,
		SessionID: p.sess.ID,
		Kind:      p.kind,
		Detail:    append(json.RawMessage(nil), p.detail...),
		Created:   p.created,
		ExpiresAt: p.expiresAt,
	}
}

// randomID returns a fresh approval id: a random hex string.
func randomID() api.ApprovalID {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return api.ApprovalID(hex.EncodeToString(b[:]))
}
