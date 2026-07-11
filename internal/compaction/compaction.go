// Package compaction implements the agent context-window compaction trigger: the
// policy that decides WHEN a session's context is full enough to compact and
// drives one of two mechanisms. It is distinct from the compaction mechanism
// (events.Compactor, which collapses a daemon-transcript range) and from the
// provider's own /compact, which shrinks the provider-side context — the trigger
// prefers the provider command and falls back to the daemon summarizer.
//
// The trigger runs after a turn completes, off the session's own idle state: it
// reads the agent's last reported context-window fullness (recorded on the
// session from ACP usage_update) and, when it crosses the threshold, forwards
// the provider's /compact command if the session advertises one, else summarizes
// the transcript beyond the retention window. Forwarding /compact runs a turn,
// which completes and triggers the check again; a per-session guard makes that
// re-entrant call a no-op so a single fill cannot loop.
package compaction

import (
	"context"
	"strings"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/session"
)

// Prompter runs a prompt turn on a session, used to forward the provider's
// /compact command. *agent.Service satisfies it. The trigger takes an interface
// so it does not import the agent package (which holds the trigger's own
// interface), avoiding an import cycle.
type Prompter interface {
	Prompt(ctx context.Context, p api.AgentPromptParams) (api.AgentPromptResult, error)
}

// Transcript compacts a completed [from, to] range of a session stream into a
// summary record. *events.Compactor satisfies it. Ranger reports the safe
// range. *events.Store satisfies Ranger.
type Transcript interface {
	Compact(ctx context.Context, streamID api.StreamID, sessionID api.SessionID, from, to uint64, budget int) error
}

// Ranger bounds the transcript range that may be compacted right now.
// *events.Store satisfies it.
type Ranger interface {
	CompactableRange(streamID api.StreamID) (from, to uint64, ok bool)
}

// compactCommand is the provider slash command that shrinks the agent's own
// context window. Providers that support it advertise it via
// available_commands_update (Claude does; Codex does not).
const compactCommand = "compact"

// Service is the post-turn compaction trigger. It is safe for concurrent use.
type Service struct {
	sessions   *session.Registry
	prompter   Prompter
	transcript Transcript
	ranger     Ranger
	threshold  float64
	budget     int

	mu     sync.Mutex
	active map[api.SessionID]struct{} // sessions currently compacting (re-entrancy guard)
}

// NewService returns a compaction trigger. threshold is the context-window
// fullness fraction (0,1] that fires a compaction; budget is the summarizer
// character budget for the daemon-transcript fallback (0 uses the summarizer
// default). prompter forwards the provider /compact; transcript+ranger drive the
// fallback.
func NewService(sessions *session.Registry, prompter Prompter, transcript Transcript, ranger Ranger, threshold float64, budget int) *Service {
	return &Service{
		sessions:   sessions,
		prompter:   prompter,
		transcript: transcript,
		ranger:     ranger,
		threshold:  threshold,
		budget:     budget,
		active:     make(map[api.SessionID]struct{}),
	}
}

// MaybeCompact triggers compaction for a session when its last reported
// context-window fullness has crossed the threshold. It is a no-op when the
// session is unknown, the provider has reported no usage, the context is below
// threshold, or the session is already compacting. It is meant to be called
// after a turn completes, with the session idle.
func (s *Service) MaybeCompact(ctx context.Context, id api.SessionID) {
	sess, ok := s.sessions.Get(id)
	if !ok {
		return
	}
	used, window, ok := sess.ContextUsage()
	if !ok {
		return // provider reports no context-window usage; nothing to act on
	}
	if float64(used)/float64(window) < s.threshold {
		return
	}
	// Guard re-entrancy: forwarding /compact runs a turn that completes and calls
	// MaybeCompact again; without this a single fill could loop until the provider
	// reports a lower usage. Also serializes concurrent triggers per session.
	if !s.begin(id) {
		return
	}
	defer s.end(id)

	if hasCompactCommand(sess.ProviderCommands()) {
		// Preferred: the provider shrinks its own context window. Forwarding it as a
		// turn is what actually reduces the agent-side context; the daemon transcript
		// is left intact (it is the durable record).
		_, _ = s.prompter.Prompt(ctx, api.AgentPromptParams{SessionID: id, Text: "/" + compactCommand})
		return
	}
	// Fallback: the provider offers no compaction command, so collapse the
	// daemon-transcript prefix beyond the retention window into a summary record.
	// This does not shrink the provider's context, but it bounds what a resume or
	// replay must carry. Nothing to do when the range is within retention.
	from, to, ok := s.ranger.CompactableRange(sess.Stream)
	if !ok {
		return
	}
	_ = s.transcript.Compact(ctx, sess.Stream, id, from, to, s.budget)
}

// begin marks a session as compacting, returning false if it already was.
func (s *Service) begin(id api.SessionID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.active[id]; busy {
		return false
	}
	s.active[id] = struct{}{}
	return true
}

func (s *Service) end(id api.SessionID) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}

// hasCompactCommand reports whether the provider advertises a /compact command.
func hasCompactCommand(cmds []api.SlashCommand) bool {
	for _, c := range cmds {
		if strings.EqualFold(c.Name, compactCommand) {
			return true
		}
	}
	return false
}
