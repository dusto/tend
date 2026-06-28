package tasks

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Task method names.
const (
	MethodCreate  = "task.create"
	MethodShow    = "task.show"
	MethodList    = "task.list"
	MethodClaim   = "task.claim"
	MethodComment = "task.comment"
	MethodClose   = "task.close"
)

// Emitter publishes events. *events.Store satisfies it.
type Emitter interface {
	Publish(api.Event) (api.Event, error)
}

// Factory creates the task provider for a workspace. Each workspace gets one
// provider, created on first use.
type Factory func(api.WorkspaceID) Provider

// Service backs the task.* methods. It keeps one provider per workspace and
// bridges each provider's change stream to task_* events on the workspace event
// stream. It is safe for concurrent use.
type Service struct {
	factory Factory
	emit    Emitter

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	providers map[api.WorkspaceID]Provider
}

// NewService returns a Service that creates providers with factory and emits
// task events through emit. Call Close to stop the event bridges.
func NewService(factory Factory, emit Emitter) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		factory:   factory,
		emit:      emit,
		ctx:       ctx,
		cancel:    cancel,
		providers: make(map[api.WorkspaceID]Provider),
	}
}

// Close stops all provider event bridges.
func (s *Service) Close() { s.cancel() }

// provider returns the workspace's provider, creating it (and starting its event
// bridge) on first use.
func (s *Service) provider(ws api.WorkspaceID) Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.providers[ws]; ok {
		return p
	}
	p := s.factory(ws)
	s.providers[ws] = p
	if ch, err := p.Events(s.ctx); err == nil {
		go s.bridge(ch)
	}
	return p
}

// bridge forwards a provider's change events to the workspace event stream until
// the channel closes (on Close).
func (s *Service) bridge(ch <-chan Event) {
	for ev := range ch {
		typ, ok := taskEventType(ev.Kind)
		if !ok {
			continue
		}
		payload, _ := json.Marshal(api.TaskChange{Ref: ev.Ref})
		_, _ = s.emit.Publish(api.Event{
			StreamID: api.WorkspaceStream(ev.Ref.WorkspaceID),
			Scope:    api.ScopeWorkspace,
			Type:     typ,
			Payload:  payload,
		})
	}
}

// taskEventType maps a provider change kind to its TEND event type.
func taskEventType(k EventKind) (string, bool) {
	switch k {
	case EventCreated:
		return "task_created", true
	case EventUpdated:
		return "task_updated", true
	case EventCommented:
		return "task_commented", true
	case EventClosed:
		return "task_closed", true
	}
	return "", false
}

// Register installs the task methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	for _, reg := range []func() error{
		func() error { return dispatch.Handle(m, MethodCreate, s.create) },
		func() error { return dispatch.Handle(m, MethodShow, s.show) },
		func() error { return dispatch.Handle(m, MethodList, s.list) },
		func() error { return dispatch.Handle(m, MethodClaim, s.claim) },
		func() error { return dispatch.Handle(m, MethodComment, s.comment) },
		func() error { return dispatch.Handle(m, MethodClose, s.close) },
	} {
		if err := reg(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) create(ctx context.Context, p api.TaskCreateParams) (api.Task, error) {
	t, err := s.provider(p.WorkspaceID).Create(ctx, CreateParams{
		Title:       p.Title,
		Description: p.Description,
		Labels:      p.Labels,
	})
	return result(t, err)
}

func (s *Service) show(ctx context.Context, p api.TaskShowParams) (api.Task, error) {
	return s.showRef(ctx, p.Ref)
}

// showRef fetches and converts one task; the mutating handlers use it to return
// the updated task after their change.
func (s *Service) showRef(ctx context.Context, ref api.TaskRef) (api.Task, error) {
	t, err := s.provider(ref.WorkspaceID).Show(ctx, ref)
	return result(t, err)
}

func (s *Service) list(ctx context.Context, p api.TaskListParams) (api.TaskListResult, error) {
	ts, err := s.provider(p.WorkspaceID).List(ctx, Filter{Status: p.Status})
	if err != nil {
		return api.TaskListResult{}, invalidParams(err)
	}
	out := make([]api.Task, 0, len(ts))
	for _, t := range ts {
		out = append(out, toAPITask(t))
	}
	return api.TaskListResult{Tasks: out}, nil
}

// List returns a workspace's tasks, optionally filtered by status, for daemon
// features that need task data outside the task.* RPC surface (slash-command
// argument completion).
func (s *Service) List(ctx context.Context, ws api.WorkspaceID, status string) ([]api.Task, error) {
	res, err := s.list(ctx, api.TaskListParams{WorkspaceID: ws, Status: status})
	return res.Tasks, err
}

func (s *Service) claim(ctx context.Context, p api.TaskClaimParams) (api.Task, error) {
	if err := s.provider(p.Ref.WorkspaceID).Claim(ctx, p.Ref, p.Assignee); err != nil {
		return api.Task{}, invalidParams(err)
	}
	return s.showRef(ctx, p.Ref)
}

func (s *Service) comment(ctx context.Context, p api.TaskCommentParams) (api.Task, error) {
	c := Comment{Author: p.Author, Text: p.Text, At: time.Now()}
	if err := s.provider(p.Ref.WorkspaceID).Comment(ctx, p.Ref, c); err != nil {
		return api.Task{}, invalidParams(err)
	}
	return s.showRef(ctx, p.Ref)
}

func (s *Service) close(ctx context.Context, p api.TaskCloseParams) (api.Task, error) {
	if err := s.provider(p.Ref.WorkspaceID).Close(ctx, p.Ref); err != nil {
		return api.Task{}, invalidParams(err)
	}
	return s.showRef(ctx, p.Ref)
}

// result converts a provider Task/error to the wire result, mapping a provider
// error (unknown or foreign ref) to invalid-params.
func result(t Task, err error) (api.Task, error) {
	if err != nil {
		return api.Task{}, invalidParams(err)
	}
	return toAPITask(t), nil
}

func invalidParams(err error) error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
}

func toAPITask(t Task) api.Task {
	out := api.Task{
		Ref:         t.Ref,
		Title:       t.Title,
		Status:      t.Status,
		Description: t.Description,
		Assignee:    t.Assignee,
		Labels:      t.Labels,
	}
	for _, c := range t.Comments {
		out.Comments = append(out.Comments, api.TaskComment{Author: c.Author, Text: c.Text, At: c.At})
	}
	return out
}
