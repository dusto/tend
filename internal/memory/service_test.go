package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// fakeProvider is an in-memory provider for service tests.
type fakeProvider struct {
	hits    []api.MemoryHit
	entries map[api.MemoryID]api.MemoryEntry
}

func (f *fakeProvider) Search(_ context.Context, _, _ string, _ int) ([]api.MemoryHit, error) {
	return f.hits, nil
}

func (f *fakeProvider) Get(_ context.Context, id api.MemoryID) (api.MemoryEntry, error) {
	if e, ok := f.entries[id]; ok {
		return e, nil
	}
	return api.MemoryEntry{}, ErrNotFound
}

func newService(p Provider) *Service {
	return NewService(func(api.WorkspaceID) Provider { return p })
}

func codeOf(t *testing.T, err error) int {
	t.Helper()
	var rerr *rpc.Error
	if !errors.As(err, &rerr) {
		t.Fatalf("error %v is not an *rpc.Error", err)
	}
	return rerr.Code
}

func TestServiceSearchRoundTrip(t *testing.T) {
	svc := newService(&fakeProvider{hits: []api.MemoryHit{{ID: "m1", Snippet: "hi"}}})
	res, err := svc.search(context.Background(), api.MemorySearchParams{WorkspaceID: "ws1", Query: "hi"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ID != "m1" {
		t.Errorf("hits = %+v", res.Hits)
	}
}

func TestServiceSearchValidation(t *testing.T) {
	svc := newService(&fakeProvider{})
	if _, err := svc.search(context.Background(), api.MemorySearchParams{Query: "x"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("missing workspace should be invalid params")
	}
	if _, err := svc.search(context.Background(), api.MemorySearchParams{WorkspaceID: "ws1"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("missing query should be invalid params")
	}
}

func TestServiceGetRoundTrip(t *testing.T) {
	svc := newService(&fakeProvider{entries: map[api.MemoryID]api.MemoryEntry{
		"m1": {ID: "m1", Title: "t", Text: "body"},
	}})
	res, err := svc.get(context.Background(), api.MemoryGetParams{WorkspaceID: "ws1", ID: "m1"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if res.Entry.Text != "body" {
		t.Errorf("entry = %+v", res.Entry)
	}
}

func TestServiceGetNotFoundIsInvalidParams(t *testing.T) {
	svc := newService(&fakeProvider{entries: map[api.MemoryID]api.MemoryEntry{}})
	if _, err := svc.get(context.Background(), api.MemoryGetParams{WorkspaceID: "ws1", ID: "nope"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("unknown id should map to invalid params")
	}
}

func TestServiceCachesProviderPerWorkspace(t *testing.T) {
	var built int
	svc := NewService(func(api.WorkspaceID) Provider {
		built++
		return &fakeProvider{}
	})
	for range 3 {
		_, _ = svc.search(context.Background(), api.MemorySearchParams{WorkspaceID: "ws1", Query: "q"})
	}
	if built != 1 {
		t.Errorf("provider built %d times, want 1 (cached per workspace)", built)
	}
}
