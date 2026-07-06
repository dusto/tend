package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// fakeProvider is an in-memory provider for service tests.
type fakeProvider struct {
	hits     []api.MemoryHit
	entries  map[api.MemoryID]api.MemoryEntry
	written  *api.MemoryWriteParams // records the last Write call
	writeErr error                  // when set, Write returns it
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

func (f *fakeProvider) Write(_ context.Context, in api.MemoryWriteParams) (api.MemoryEntry, error) {
	f.written = &in
	if f.writeErr != nil {
		return api.MemoryEntry{}, f.writeErr
	}
	id := in.ID
	if id == "" {
		id = "generated"
	}
	return api.MemoryEntry{
		ID: id, WorkspaceID: in.WorkspaceID, Kind: in.Kind,
		Title: in.Title, Tags: in.Tags, Task: in.Task, Text: in.Text,
	}, nil
}

// fakeEmitter records the events a service publishes.
type fakeEmitter struct{ events []api.Event }

func (e *fakeEmitter) Publish(ev api.Event) (api.Event, error) {
	e.events = append(e.events, ev)
	return ev, nil
}

func newService(p Provider) *Service {
	return NewService(func(api.WorkspaceID) Provider { return p }, nil)
}

func newServiceEmit(p Provider, emit Emitter) *Service {
	return NewService(func(api.WorkspaceID) Provider { return p }, emit)
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

func TestServiceWriteRoundTrip(t *testing.T) {
	fp := &fakeProvider{}
	svc := newService(fp)
	res, err := svc.write(context.Background(), api.MemoryWriteParams{
		WorkspaceID: "ws1", ID: "m1", Title: "t", Text: "body",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if res.Entry.ID != "m1" || res.Entry.Text != "body" {
		t.Errorf("entry = %+v", res.Entry)
	}
	if fp.written == nil || fp.written.Title != "t" {
		t.Errorf("provider Write not called with params: %+v", fp.written)
	}
}

func TestServiceWriteValidation(t *testing.T) {
	svc := newService(&fakeProvider{})
	if _, err := svc.write(context.Background(), api.MemoryWriteParams{Text: "x"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("missing workspace should be invalid params")
	}
	if _, err := svc.write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("empty title and text should be invalid params")
	}
}

func TestServiceWriteInvalidIDIsInvalidParams(t *testing.T) {
	emit := &fakeEmitter{}
	svc := newServiceEmit(&fakeProvider{writeErr: ErrInvalidID}, emit)
	_, err := svc.write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "../escape", Text: "x"})
	if codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("unsafe id should map to invalid params, got %v", err)
	}
	// A rejected write emits nothing.
	if len(emit.events) != 0 {
		t.Errorf("rejected write emitted %d events, want 0", len(emit.events))
	}
}

func TestServiceWriteEmitsMemoryWritten(t *testing.T) {
	emit := &fakeEmitter{}
	svc := newServiceEmit(&fakeProvider{}, emit)
	if _, err := svc.write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "m1", Title: "t", Text: "b"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(emit.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(emit.events))
	}
	ev := emit.events[0]
	if ev.Type != "memory_written" || ev.Scope != api.ScopeWorkspace || ev.StreamID != api.WorkspaceStream("ws1") {
		t.Errorf("event envelope = %+v", ev)
	}
	var got api.MemoryWritten
	if err := json.Unmarshal(ev.Payload, &got); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if got.ID != "m1" || got.WorkspaceID != "ws1" || got.Title != "t" {
		t.Errorf("payload = %+v", got)
	}
}

func TestServiceSearchEmitsMemorySearched(t *testing.T) {
	emit := &fakeEmitter{}
	svc := newServiceEmit(&fakeProvider{hits: []api.MemoryHit{{ID: "a"}, {ID: "b"}}}, emit)
	if _, err := svc.search(context.Background(), api.MemorySearchParams{WorkspaceID: "ws1", Query: "q", Kind: "note"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(emit.events) != 1 || emit.events[0].Type != "memory_searched" {
		t.Fatalf("events = %+v, want one memory_searched", emit.events)
	}
	var got api.MemorySearched
	if err := json.Unmarshal(emit.events[0].Payload, &got); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if got.Query != "q" || got.Kind != "note" || got.Results != 2 {
		t.Errorf("payload = %+v, want query=q kind=note results=2", got)
	}
}

func TestServiceNilEmitterIsSafe(t *testing.T) {
	svc := newService(&fakeProvider{}) // emit == nil
	if _, err := svc.write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "m1", Text: "b"}); err != nil {
		t.Errorf("write with nil emitter: %v", err)
	}
	if _, err := svc.search(context.Background(), api.MemorySearchParams{WorkspaceID: "ws1", Query: "q"}); err != nil {
		t.Errorf("search with nil emitter: %v", err)
	}
}

func TestServiceCachesProviderPerWorkspace(t *testing.T) {
	var built int
	svc := NewService(func(api.WorkspaceID) Provider {
		built++
		return &fakeProvider{}
	}, nil)
	for range 3 {
		_, _ = svc.search(context.Background(), api.MemorySearchParams{WorkspaceID: "ws1", Query: "q"})
	}
	if built != 1 {
		t.Errorf("provider built %d times, want 1 (cached per workspace)", built)
	}
}
