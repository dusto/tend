// Package compaction implements the agent context-window compaction trigger: the
// policy that decides WHEN a session's context is full enough to compact and
// drives BOTH sides of it. There are two contexts to shrink, and it addresses
// each with its own mechanism: the provider-side context (the agent's own
// conversation) via a forwarded /compact or /clear command, and TEND's durable
// transcript (which resume/replay is built from) via events.Compactor. Shrinking
// only the agent while TEND still holds the full history would re-inflate the
// context on the next resume, so both run together — not one as a fallback for
// the other.
//
// The trigger runs after a turn completes, off the session's own idle state: it
// reads the agent's last reported context-window fullness (recorded on the
// session from ACP usage_update) and, when it crosses the threshold, forwards the
// provider's context-shrinking command if the session advertises one and compacts
// the daemon transcript beyond the retention window. Forwarding a command runs a
// turn, which completes and triggers the check again; a per-session guard makes
// that re-entrant call a no-op so a single fill cannot loop.
package compaction

import (
	"context"
	"strings"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/session"
)

// Prompter runs a prompt turn on a session, used to forward the provider's
// context-shrinking command. *agent.Service satisfies it. The trigger takes an
// interface so it does not import the agent package (which holds the trigger's
// own interface), avoiding an import cycle.
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

// compactCommands are the provider slash commands that shrink the agent's own
// context window, in preference order. Providers advertise the ones they support
// via available_commands_update (Claude offers both; Codex offers neither).
// /compact condenses the conversation and is preferred; /clear discards it
// outright — more aggressive, but TEND's own transcript compaction in the same
// pass keeps a resumable condensed record, so the working context is not lost.
var compactCommands = []string{"compact", "clear"}

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
// character budget for the daemon-transcript compaction (0 uses the summarizer
// default). prompter forwards the provider command; transcript+ranger compact the
// daemon transcript.
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

	// Shrink the agent's own context window when the provider offers a command.
	// Forwarding it as a turn is what actually reduces the agent-side context.
	if cmd, ok := pickCompactCommand(sess.ProviderCommands()); ok {
		_, _ = s.prompter.Prompt(ctx, api.AgentPromptParams{SessionID: id, Text: "/" + cmd})
	}
	// Compact the daemon-side transcript too, whether or not the agent has its own
	// command: collapse the prefix beyond the retention window into a summary
	// record. Compaction is non-destructive — the raw records are retained and
	// replay serves the summary in their place — so this loses nothing durable; it
	// bounds what a resume or replay (and any resume_seed built from them) must
	// carry. Shrinking the agent while TEND still holds the full history would
	// re-inflate the context on the next resume. Nothing to do when the compactable
	// range is within retention.
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

// pickCompactCommand returns the most-preferred context-shrinking command the
// provider advertises (see compactCommands), or ok=false when it advertises
// none. Preference wins over advertised order: /compact is chosen over /clear
// whenever both are offered.
func pickCompactCommand(cmds []api.SlashCommand) (string, bool) {
	for _, want := range compactCommands {
		for _, c := range cmds {
			if strings.EqualFold(c.Name, want) {
				return want, true
			}
		}
	}
	return "", false
}
