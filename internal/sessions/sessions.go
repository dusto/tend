// Package sessions implements the session.* query and focus methods: session.list
// reports every daemon session with the state needed to render a session
// overview and route to it, and session.claim moves a session's editor binding
// to the calling client so editor-local tools follow the focused session. The
// daemon owns sessions (they outlive any client), so these read the shared
// session registry and drive the editor binder; the heavy lifecycle lives in
// the agent service.
package sessions

import (
	"encoding/json"
	"sort"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/session"
)

// Method names.
const (
	MethodList   = "session.list"
	MethodClaim  = "session.claim"
	MethodRename = "session.rename"
)

// Publisher receives daemon-side session events (session.rename emits
// session_renamed). *events.Store satisfies it. Optional: nil skips the event
// but still records the label on the session.
type Publisher interface {
	Publish(api.Event) (api.Event, error)
}

// Service backs the session.* methods over the session registry and the editor
// binder. It is safe for concurrent use (its state is the shared registries').
type Service struct {
	sessions *session.Registry
	binder   *editor.Binder
	pub      Publisher
}

// NewService returns a Service reading sessions and driving binder. pub receives
// session-change events (session_renamed) and may be nil.
func NewService(sessions *session.Registry, binder *editor.Binder, pub Publisher) *Service {
	return &Service{sessions: sessions, binder: binder, pub: pub}
}

// info builds the listed view of one session as seen by the client caller
// (caller is "" for an unauthenticated/list-all read, in which case EditorBound
// is reported against no client and so is always false).
func info(s *session.Session, caller api.ClientID) api.SessionInfo {
	owner, bound := s.Owner()
	out := api.SessionInfo{
		SessionID:    s.ID,
		ProviderID:   s.ProviderID,
		WorkspaceID:  s.WorkspaceID,
		WorktreeRoot: s.WorktreeRoot,
		StreamID:     s.Stream,
		Status:       s.Status(),
		Label:        s.Label(),
		EditorBound:  bound && owner == caller,
	}
	if s.HasTask() {
		task := s.Task
		out.Task = &task
	}
	if p, ok := s.Pending(); ok {
		out.Pending = &api.SessionPending{Kind: p.Kind, ID: p.ID}
	}
	out.CurrentModeID, out.AvailableModes = s.Modes()
	out.CurrentModelID, out.AvailableModels = s.Models()
	out.CurrentThoughtLevelID, out.AvailableThoughtLevels = s.ThoughtLevels()
	out.ResourceUsage = s.ResourceUsage()
	return out
}

// List returns the daemon's sessions, optionally filtered to one workspace,
// ordered by session id. caller is the requesting client, used to report which
// sessions it holds the editor binding for.
func (svc *Service) List(caller api.ClientID, p api.SessionListParams) api.SessionListResult {
	all := svc.sessions.List()
	out := make([]api.SessionInfo, 0, len(all))
	for _, s := range all {
		if p.WorkspaceID != "" && s.WorkspaceID != p.WorkspaceID {
			continue
		}
		out = append(out, info(s, caller))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return api.SessionListResult{Sessions: out}
}

// Claim moves the session's editor binding to caller and returns the session's
// updated view. The binder enforces that caller is an editor-capable connected
// client and that the session exists.
func (svc *Service) Claim(caller api.ClientID, p api.SessionClaimParams) (api.SessionClaimResult, error) {
	if err := svc.binder.Claim(p.SessionID, caller); err != nil {
		return api.SessionClaimResult{}, err
	}
	s, ok := svc.sessions.Get(p.SessionID)
	if !ok {
		// Claim succeeded against a session the registry no longer has — treat as
		// gone rather than returning a half-built view.
		return api.SessionClaimResult{}, editor.ErrNoSession
	}
	return api.SessionClaimResult{Session: info(s, caller)}, nil
}

// Rename sets or clears a session's user-facing label and returns its updated
// view. An empty label clears it; a label longer than api.MaxSessionLabelLen is
// rejected. On success it emits session_renamed so attached clients see the
// change. The label is daemon-side session state (it does not touch the
// provider), so this needs no editor binding.
func (svc *Service) Rename(caller api.ClientID, p api.SessionRenameParams) (api.SessionRenameResult, error) {
	if len(p.Label) > api.MaxSessionLabelLen {
		return api.SessionRenameResult{}, errLabelTooLong
	}
	s, ok := svc.sessions.Get(p.SessionID)
	if !ok {
		return api.SessionRenameResult{}, editor.ErrNoSession
	}
	s.SetLabel(p.Label)
	if svc.pub != nil {
		raw, _ := json.Marshal(api.SessionRenamed(p))
		_, _ = svc.pub.Publish(api.Event{
			StreamID: s.Stream,
			Scope:    api.ScopeSession,
			Type:     "session_renamed",
			Payload:  raw,
		})
	}
	return api.SessionRenameResult{Session: info(s, caller)}, nil
}
