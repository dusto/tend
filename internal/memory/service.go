package memory

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Method names, matching the api contract.
const (
	MethodSearch   = "memory.search"
	MethodGet      = "memory.get"
	MethodWrite    = "memory.write"
	MethodSteering = "memory.steering"
)

// Emitter publishes events on the workspace event stream. *events.Store
// satisfies it. A nil Emitter disables emission (memory still works).
type Emitter interface {
	Publish(api.Event) (api.Event, error)
}

// Service backs the memory.* methods. It keeps one provider per workspace, built
// on first use from the factory, and emits memory_written/memory_searched events
// on the workspace stream. It is safe for concurrent use.
type Service struct {
	factory Factory
	emit    Emitter

	mu        sync.Mutex
	providers map[api.WorkspaceID]Provider
}

// NewService returns a Service that builds providers with factory and emits
// memory events through emit (nil disables emission).
func NewService(factory Factory, emit Emitter) *Service {
	return &Service{factory: factory, emit: emit, providers: make(map[api.WorkspaceID]Provider)}
}

// Register installs the memory.* methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	if err := dispatch.Handle(m, MethodSearch, s.search); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodGet, s.get); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodWrite, s.write); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodSteering, s.steering)
}

func (s *Service) provider(ws api.WorkspaceID) Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.providers[ws]; ok {
		return p
	}
	p := s.factory(ws)
	s.providers[ws] = p
	return p
}

func (s *Service) search(ctx context.Context, p api.MemorySearchParams) (api.MemorySearchResult, error) {
	if p.WorkspaceID == "" {
		return api.MemorySearchResult{}, invalidParams("workspace_id is required")
	}
	if p.Query == "" {
		return api.MemorySearchResult{}, invalidParams("query is required")
	}
	hits, err := s.provider(p.WorkspaceID).Search(ctx, p.Query, p.Kind, p.Limit)
	if err != nil {
		return api.MemorySearchResult{}, internalErr(err)
	}
	s.publish(api.WorkspaceStream(p.WorkspaceID), "memory_searched", api.MemorySearched{
		WorkspaceID: p.WorkspaceID,
		Query:       p.Query,
		Kind:        p.Kind,
		Results:     len(hits),
	})
	return api.MemorySearchResult{Hits: hits}, nil
}

func (s *Service) get(ctx context.Context, p api.MemoryGetParams) (api.MemoryGetResult, error) {
	if p.WorkspaceID == "" {
		return api.MemoryGetResult{}, invalidParams("workspace_id is required")
	}
	if p.ID == "" {
		return api.MemoryGetResult{}, invalidParams("id is required")
	}
	e, err := s.provider(p.WorkspaceID).Get(ctx, p.ID)
	if errors.Is(err, ErrNotFound) {
		return api.MemoryGetResult{}, invalidParams(err.Error())
	}
	if err != nil {
		return api.MemoryGetResult{}, internalErr(err)
	}
	return api.MemoryGetResult{Entry: e}, nil
}

func (s *Service) write(ctx context.Context, p api.MemoryWriteParams) (api.MemoryWriteResult, error) {
	if p.WorkspaceID == "" {
		return api.MemoryWriteResult{}, invalidParams("workspace_id is required")
	}
	if p.Title == "" && p.Text == "" {
		return api.MemoryWriteResult{}, invalidParams("title or text is required")
	}
	e, err := s.provider(p.WorkspaceID).Write(ctx, p)
	if errors.Is(err, ErrInvalidID) || errors.Is(err, ErrInvalidApply) {
		return api.MemoryWriteResult{}, invalidParams(err.Error())
	}
	if err != nil {
		return api.MemoryWriteResult{}, internalErr(err)
	}
	s.publish(api.WorkspaceStream(e.WorkspaceID), "memory_written", api.MemoryWritten{
		WorkspaceID: e.WorkspaceID,
		ID:          e.ID,
		Kind:        e.Kind,
		Title:       e.Title,
		Task:        e.Task,
	})
	return api.MemoryWriteResult{Entry: e}, nil
}

func (s *Service) steering(ctx context.Context, p api.MemorySteeringParams) (api.MemorySteeringResult, error) {
	if p.WorkspaceID == "" {
		return api.MemorySteeringResult{}, invalidParams("workspace_id is required")
	}
	entries, err := s.provider(p.WorkspaceID).Steering(ctx, p.Path)
	if err != nil {
		return api.MemorySteeringResult{}, internalErr(err)
	}
	return api.MemorySteeringResult{Entries: entries}, nil
}

// publish emits one workspace event, marshaling payload. Emission is best-effort:
// a nil emitter or a marshal/publish error never fails the originating call.
func (s *Service) publish(stream api.StreamID, typ string, payload any) {
	if s.emit == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = s.emit.Publish(api.Event{
		StreamID: stream,
		Scope:    api.ScopeWorkspace,
		Type:     typ,
		Payload:  body,
	})
}

func invalidParams(msg string) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "memory: " + msg}
}

func internalErr(err error) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
}
