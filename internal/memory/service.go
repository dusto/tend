package memory

import (
	"context"
	"errors"
	"sync"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Method names, matching the api contract.
const (
	MethodSearch = "memory.search"
	MethodGet    = "memory.get"
)

// Service backs the memory.* methods. It keeps one provider per workspace, built
// on first use from the factory, and is safe for concurrent use.
type Service struct {
	factory Factory

	mu        sync.Mutex
	providers map[api.WorkspaceID]Provider
}

// NewService returns a Service that builds providers with factory.
func NewService(factory Factory) *Service {
	return &Service{factory: factory, providers: make(map[api.WorkspaceID]Provider)}
}

// Register installs the memory.* methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	if err := dispatch.Handle(m, MethodSearch, s.search); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodGet, s.get)
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
	hits, err := s.provider(p.WorkspaceID).Search(ctx, p.Query, p.Limit)
	if err != nil {
		return api.MemorySearchResult{}, internalErr(err)
	}
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

func invalidParams(msg string) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "memory: " + msg}
}

func internalErr(err error) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
}
