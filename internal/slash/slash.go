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
	"fmt"
	"strings"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
	"github.com/dusto/tend/internal/tasks"
)

// Method names.
const (
	MethodList     = "slash.list"
	MethodComplete = "slash.complete"
	MethodInvoke   = "slash.invoke"
)

// maxCandidates caps a completion response so a large task list cannot produce
// an unbounded payload.
const maxCandidates = 50

// Publisher receives the slash_commands_updated event. *events.Store satisfies it.
type Publisher interface {
	Publish(api.Event) (api.Event, error)
}

// Tasks is the task-provider slice the slash service drives: listing for
// argument completion, and the actions the daemon task commands invoke. It deals
// in workspace + bare id (the provider name stays inside the task service).
// *tasks.Service satisfies it; the narrow interface keeps the slash service
// testable without a task provider.
type Tasks interface {
	List(ctx context.Context, ws api.WorkspaceID, status string) ([]api.Task, error)
	Show(ctx context.Context, ws api.WorkspaceID, id string) (api.Task, error)
	Claim(ctx context.Context, ws api.WorkspaceID, id, assignee string) (api.Task, error)
	Comment(ctx context.Context, ws api.WorkspaceID, id, author, text string) (api.Task, error)
	CloseTask(ctx context.Context, ws api.WorkspaceID, id string) (api.Task, error)
}

// Prompter runs a prompt turn on a session, used to forward a non-daemon command
// to the agent as prompt text. *agent.Service satisfies it.
type Prompter interface {
	Prompt(ctx context.Context, p api.AgentPromptParams) (api.AgentPromptResult, error)
}

// argKind classifies what a daemon command's argument completes against.
type argKind int

const (
	argNone argKind = iota
	argTaskID
	argStatus
)

// daemonArgKinds maps each daemon command to how its leading argument completes.
// A command absent here (or any provider command) gets no daemon completion.
var daemonArgKinds = map[string]argKind{
	"tasks":   argStatus,
	"task":    argTaskID,
	"claim":   argTaskID,
	"comment": argTaskID,
	"close":   argTaskID,
}

// Service backs slash.list and the slash_commands_updated event. It holds the
// static daemon commands and reads the agent's advertised commands from the
// session registry, so per-session provider commands are dropped with the session
// (no separate bookkeeping to leak). It is safe for concurrent use.
type Service struct {
	sessions *session.Registry
	tasks    Tasks
	prompter Prompter
	pub      Publisher
	daemon   []api.SlashCommand
}

// NewService returns a Service that reads sessions, runs daemon task commands
// through tasks, forwards other commands through prompter, emits through pub, and
// offers the built-in daemon commands.
func NewService(sessions *session.Registry, tasks Tasks, prompter Prompter, pub Publisher) *Service {
	return &Service{sessions: sessions, tasks: tasks, prompter: prompter, pub: pub, daemon: daemonCommands()}
}

// Register installs the slash.* methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	if err := dispatch.Handle(m, MethodList, s.List); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodComplete, s.Complete); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodInvoke, s.Invoke)
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

// Complete returns the candidates for a command's argument matching the prefix.
// It completes only the daemon commands' arguments — task ids for the
// task-tracking commands, status names for /tasks — resolving the workspace from
// the session. A provider or unknown command, or an unknown session, yields no
// candidates (not an error, so a client may call it optimistically).
func (s *Service) Complete(ctx context.Context, p api.SlashCompleteParams) (api.SlashCompleteResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return noCandidates(), nil
	}
	switch daemonArgKinds[p.Command] {
	case argTaskID:
		return s.completeTaskIDs(ctx, sess.WorkspaceID, p.Prefix)
	case argStatus:
		return completeStatus(p.Prefix), nil
	default:
		return noCandidates(), nil
	}
}

// Invoke runs a slash command. A command the daemon owns runs its task action
// (resolving the workspace from the session); any other command — a provider
// command or one the daemon does not recognize — is forwarded to the agent as a
// prompt turn ("/command args"), per the dispatch rule that the daemon handles
// what it knows and sends the rest on.
func (s *Service) Invoke(ctx context.Context, p api.SlashInvokeParams) (api.SlashInvokeResult, error) {
	if p.Command == "" {
		return api.SlashInvokeResult{}, invalidParams("command is required")
	}
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.SlashInvokeResult{}, unknownSession(p.SessionID)
	}
	if s.ownsCommand(p.Command) {
		return s.invokeDaemon(ctx, sess.WorkspaceID, p.Command, strings.TrimSpace(p.Args))
	}
	return s.forward(ctx, p)
}

// invokeDaemon runs a daemon task command against the workspace and returns its
// result. The argument shape follows each command's hint.
func (s *Service) invokeDaemon(ctx context.Context, ws api.WorkspaceID, command, args string) (api.SlashInvokeResult, error) {
	if s.tasks == nil {
		return api.SlashInvokeResult{}, internalErr("slash: no task provider")
	}
	switch command {
	case "tasks":
		ts, err := s.tasks.List(ctx, ws, args) // args is an optional status filter
		if err != nil {
			return api.SlashInvokeResult{}, err
		}
		return api.SlashInvokeResult{Origin: api.SlashOriginDaemon, Tasks: ts, Message: fmt.Sprintf("%d task(s)", len(ts))}, nil
	case "task":
		if args == "" {
			return api.SlashInvokeResult{}, invalidParams("task: a task id is required")
		}
		t, err := s.tasks.Show(ctx, ws, args)
		if err != nil {
			return api.SlashInvokeResult{}, err
		}
		return daemonTask(&t, ""), nil
	case "claim":
		if args == "" {
			return api.SlashInvokeResult{}, invalidParams("claim: a task id is required")
		}
		t, err := s.tasks.Claim(ctx, ws, args, "")
		if err != nil {
			return api.SlashInvokeResult{}, err
		}
		return daemonTask(&t, "Claimed "+args), nil
	case "comment":
		id, text := splitFirst(args)
		if id == "" || text == "" {
			return api.SlashInvokeResult{}, invalidParams("comment: a task id and comment text are required")
		}
		t, err := s.tasks.Comment(ctx, ws, id, "", text)
		if err != nil {
			return api.SlashInvokeResult{}, err
		}
		return daemonTask(&t, "Commented on "+id), nil
	case "close":
		if args == "" {
			return api.SlashInvokeResult{}, invalidParams("close: a task id is required")
		}
		t, err := s.tasks.CloseTask(ctx, ws, args)
		if err != nil {
			return api.SlashInvokeResult{}, err
		}
		return daemonTask(&t, "Closed "+args), nil
	default:
		// ownsCommand gated this, so an unhandled owned command is a daemon bug.
		return api.SlashInvokeResult{}, internalErr("slash: unhandled daemon command " + command)
	}
}

// forward sends a non-daemon command to the agent as a prompt turn, reconstructing
// the "/command args" text the agent parses.
func (s *Service) forward(ctx context.Context, p api.SlashInvokeParams) (api.SlashInvokeResult, error) {
	if s.prompter == nil {
		return api.SlashInvokeResult{}, internalErr("slash: no agent prompt path")
	}
	text := "/" + p.Command
	if args := strings.TrimSpace(p.Args); args != "" {
		text += " " + args
	}
	res, err := s.prompter.Prompt(ctx, api.AgentPromptParams{SessionID: p.SessionID, Text: text})
	if err != nil {
		return api.SlashInvokeResult{}, err
	}
	return api.SlashInvokeResult{Origin: api.SlashOriginProvider, StopReason: res.StopReason}, nil
}

// ownsCommand reports whether name is one of the daemon's own commands.
func (s *Service) ownsCommand(name string) bool {
	for _, c := range s.daemon {
		if c.Name == name {
			return true
		}
	}
	return false
}

// daemonTask builds a daemon-origin invoke result for a single task, defaulting
// the message to "<id>: <title>" when none is given.
func daemonTask(t *api.Task, message string) api.SlashInvokeResult {
	if message == "" {
		message = string(t.Ref.ID)
		if t.Title != "" {
			message += ": " + t.Title
		}
	}
	return api.SlashInvokeResult{Origin: api.SlashOriginDaemon, Task: t, Message: message}
}

// splitFirst splits s into its first whitespace-delimited token and the trimmed
// remainder.
func splitFirst(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// completeTaskIDs lists the workspace's tasks and returns those whose id has the
// prefix (case-insensitive), as id/title candidates, capped at maxCandidates.
func (s *Service) completeTaskIDs(ctx context.Context, ws api.WorkspaceID, prefix string) (api.SlashCompleteResult, error) {
	if s.tasks == nil {
		return noCandidates(), nil
	}
	ts, err := s.tasks.List(ctx, ws, "")
	if err != nil {
		return api.SlashCompleteResult{}, &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}
	low := strings.ToLower(prefix)
	out := make([]api.SlashCandidate, 0, len(ts))
	for _, t := range ts {
		if !strings.HasPrefix(strings.ToLower(t.Ref.ID), low) {
			continue
		}
		out = append(out, api.SlashCandidate{Value: t.Ref.ID, Detail: t.Title})
		if len(out) >= maxCandidates {
			break
		}
	}
	return api.SlashCompleteResult{Candidates: out}, nil
}

// completeStatus returns the task statuses matching the prefix (case-insensitive).
func completeStatus(prefix string) api.SlashCompleteResult {
	low := strings.ToLower(prefix)
	out := []api.SlashCandidate{}
	for _, st := range []string{tasks.StatusOpen, tasks.StatusInProgress, tasks.StatusClosed} {
		if strings.HasPrefix(st, low) {
			out = append(out, api.SlashCandidate{Value: st})
		}
	}
	return api.SlashCompleteResult{Candidates: out}
}

// noCandidates is an empty completion result with a non-nil slice, so it marshals
// as the schema-required "candidates": [] rather than null.
func noCandidates() api.SlashCompleteResult {
	return api.SlashCompleteResult{Candidates: []api.SlashCandidate{}}
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

func invalidParams(msg string) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "slash: " + msg}
}

func internalErr(msg string) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInternalError, Message: msg}
}

func unknownSession(id api.SessionID) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "slash: unknown session " + string(id)}
}
