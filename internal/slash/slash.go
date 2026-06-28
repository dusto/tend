// Package slash makes the daemon the single source of a session's slash
// commands. It aggregates the agent's own advertised commands (which arrive over
// ACP as available_commands_update and are stored on the session) with a static
// set of daemon/harness commands (the beads/kata task-tracking commands), and
// exposes the merged set two ways: a slash_commands_updated event emitted when
// the agent's commands change, and the slash.list query. Argument completion
// (slash.complete) and invocation (slash.invoke) build on this set separately.
package slash

import (
	"context"
	"encoding/json"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/session"
)

// MethodList is the slash.list method name.
const MethodList = "slash.list"

// Publisher receives the slash_commands_updated event. *events.Store satisfies it.
type Publisher interface {
	Publish(api.Event) (api.Event, error)
}

// Service backs slash.list and the slash_commands_updated event. It holds the
// static daemon commands and reads the agent's advertised commands from the
// session registry, so per-session provider commands are dropped with the session
// (no separate bookkeeping to leak). It is safe for concurrent use.
type Service struct {
	sessions *session.Registry
	pub      Publisher
	daemon   []api.SlashCommand
}

// NewService returns a Service that reads sessions, emits through pub, and offers
// the built-in daemon commands.
func NewService(sessions *session.Registry, pub Publisher) *Service {
	return &Service{sessions: sessions, pub: pub, daemon: daemonCommands()}
}

// Register installs the slash.* methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	return dispatch.Handle(m, MethodList, s.List)
}

// SetSessionCommands records the agent's advertised commands for a session and
// emits slash_commands_updated with the merged set. It is the acp.CommandSink the
// normalizer calls on an available_commands_update. An unknown session is a no-op
// — a notification can outlive the session it names.
func (s *Service) SetSessionCommands(id api.SessionID, providerCmds []api.SlashCommand) {
	sess, ok := s.sessions.Get(id)
	if !ok {
		return
	}
	sess.SetProviderCommands(providerCmds)
	merged := s.merge(providerCmds)
	if s.pub != nil {
		_, _ = s.pub.Publish(sessionEvent(id, "slash_commands_updated", api.SlashCommandsUpdated{
			SessionID: id,
			Commands:  merged,
		}))
	}
}

// List returns the merged slash-command set for a session: the daemon commands
// followed by the session's provider commands. An unknown session still returns
// the daemon commands (they are available before the agent advertises anything).
func (s *Service) List(_ context.Context, p api.SlashListParams) (api.SlashListResult, error) {
	var provider []api.SlashCommand
	if sess, ok := s.sessions.Get(p.SessionID); ok {
		provider = sess.ProviderCommands()
	}
	return api.SlashListResult{Commands: s.merge(provider)}, nil
}

// merge returns the daemon commands followed by the provider commands as a fresh
// slice (callers must not alias the service's daemon slice). Daemon commands are
// authoritative: a provider command whose name collides with a daemon command is
// dropped, so the merged set has no duplicate names and every name routes
// unambiguously (the daemon owns the colliding name; see the invoke bead).
func (s *Service) merge(provider []api.SlashCommand) []api.SlashCommand {
	owned := make(map[string]struct{}, len(s.daemon))
	for _, c := range s.daemon {
		owned[c.Name] = struct{}{}
	}
	out := make([]api.SlashCommand, 0, len(s.daemon)+len(provider))
	out = append(out, s.daemon...)
	for _, c := range provider {
		if _, clash := owned[c.Name]; clash {
			continue
		}
		out = append(out, c)
	}
	return out
}

// daemonCommands is the built-in daemon/harness command set: the beads/kata
// task-tracking commands, mapping to the task.* actions. Invocation and argument
// completion are layered on separately; this declares names, descriptions, and
// argument hints for the merged set.
func daemonCommands() []api.SlashCommand {
	return []api.SlashCommand{
		{Name: "tasks", Description: "List the workspace's tasks", Origin: api.SlashOriginDaemon, ArgHint: "[status]"},
		{Name: "task", Description: "Show a task by id", Origin: api.SlashOriginDaemon, ArgHint: "<task-id>"},
		{Name: "claim", Description: "Claim a task and mark it in progress", Origin: api.SlashOriginDaemon, ArgHint: "<task-id>"},
		{Name: "comment", Description: "Comment on a task", Origin: api.SlashOriginDaemon, ArgHint: "<task-id> <text>"},
		{Name: "close", Description: "Close a task", Origin: api.SlashOriginDaemon, ArgHint: "<task-id>"},
	}
}

func sessionEvent(id api.SessionID, typ string, payload any) api.Event {
	raw, _ := json.Marshal(payload)
	return api.Event{
		StreamID: api.SessionStream(id),
		Scope:    api.ScopeSession,
		Type:     typ,
		Payload:  raw,
	}
}
