package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/summarize"
)

// fakeProvider is an in-memory provider for service tests.
type fakeProvider struct {
	hits      []api.MemoryHit
	entries   map[api.MemoryID]api.MemoryEntry
	written   *api.MemoryWriteParams // records the last Write call
	writeErr  error                  // when set, Write returns it
	steering  []api.MemoryEntry      // returned by Steering
	steerPath string                 // records the last Steering path
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

func (f *fakeProvider) Steering(_ context.Context, path string) ([]api.MemoryEntry, error) {
	f.steerPath = path
	return f.steering, nil
}

// fakeEmitter records the events a service publishes.
type fakeEmitter struct{ events []api.Event }

func (e *fakeEmitter) Publish(ev api.Event) (api.Event, error) {
	e.events = append(e.events, ev)
	return ev, nil
}

func newService(p Provider) *Service {
	return NewService(func(api.WorkspaceID) Provider { return p }, nil, nil)
}

func newServiceEmit(p Provider, emit Emitter) *Service {
	return NewService(func(api.WorkspaceID) Provider { return p }, emit, nil)
}

func newServiceSum(p Provider, sum summarize.Summarizer) *Service {
	return NewService(func(api.WorkspaceID) Provider { return p }, nil, sum)
}

// fakeSummarizer records its request and returns a canned condensed result, so a
// test can assert what was assembled and that condensation was invoked.
type fakeSummarizer struct {
	reply string
	got   summarize.Request
	calls int
}

func (f *fakeSummarizer) Summarize(_ context.Context, req summarize.Request) (summarize.Result, error) {
	f.calls++
	f.got = req
	return summarize.Result{Text: f.reply, Summarized: true}, nil
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

func TestServiceWriteInvalidApplyIsInvalidParams(t *testing.T) {
	svc := newService(&fakeProvider{writeErr: ErrInvalidApply})
	_, err := svc.write(context.Background(), api.MemoryWriteParams{
		WorkspaceID: "ws1", ID: "s", Kind: api.MemoryKindSteering, Apply: "bogus", Text: "x",
	})
	if codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("invalid apply should map to invalid params, got %v", err)
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

func TestServiceSteeringRoundTrip(t *testing.T) {
	fp := &fakeProvider{steering: []api.MemoryEntry{{ID: "s1", Kind: api.MemoryKindSteering, Text: "rule"}}}
	svc := newService(fp)
	res, err := svc.steering(context.Background(), api.MemorySteeringParams{WorkspaceID: "ws1", Path: "a.go"})
	if err != nil {
		t.Fatalf("steering: %v", err)
	}
	if len(res.Entries) != 1 || res.Entries[0].ID != "s1" {
		t.Errorf("entries = %+v", res.Entries)
	}
	if fp.steerPath != "a.go" {
		t.Errorf("provider got path %q, want a.go", fp.steerPath)
	}
}

func TestServiceSteeringValidation(t *testing.T) {
	svc := newService(&fakeProvider{})
	if _, err := svc.steering(context.Background(), api.MemorySteeringParams{Path: "a.go"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("missing workspace should be invalid params")
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
	}, nil, nil)
	for range 3 {
		_, _ = svc.search(context.Background(), api.MemorySearchParams{WorkspaceID: "ws1", Query: "q"})
	}
	if built != 1 {
		t.Errorf("provider built %d times, want 1 (cached per workspace)", built)
	}
}

func TestContextRequiresWorkspace(t *testing.T) {
	svc := newService(&fakeProvider{})
	if _, err := svc.Context(context.Background(), api.MemoryContextParams{}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Error("missing workspace_id should be invalid params")
	}
}

func TestContextAssemblesSteeringWithinBudget(t *testing.T) {
	// Steering only, small enough to pass through the fallback verbatim.
	p := &fakeProvider{steering: []api.MemoryEntry{
		{ID: "s1", Title: "Rule one", Text: "always do X", Kind: api.MemoryKindSteering},
		{ID: "s2", Text: "never do Y", Kind: api.MemoryKindSteering},
	}}
	svc := newService(p) // nil summarizer -> deterministic fallback
	res, err := svc.Context(context.Background(), api.MemoryContextParams{WorkspaceID: "ws", Path: "a/b.go"})
	if err != nil {
		t.Fatal(err)
	}
	if p.steerPath != "a/b.go" {
		t.Errorf("steering path = %q, want the passed path", p.steerPath)
	}
	if res.Summarized {
		t.Error("within-budget assembly should not be summarized")
	}
	if len(res.Included) != 2 || res.Included[0] != "s1" || res.Included[1] != "s2" {
		t.Errorf("included = %v, want [s1 s2]", res.Included)
	}
	for _, want := range []string{"Rule one", "always do X", "# Steering: s2", "never do Y"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("assembled text missing %q:\n%s", want, res.Text)
		}
	}
}

func TestContextIncludesQueryNotesAfterSteering(t *testing.T) {
	p := &fakeProvider{
		steering: []api.MemoryEntry{{ID: "s1", Text: "steer", Kind: api.MemoryKindSteering}},
		hits:     []api.MemoryHit{{ID: "n1"}, {ID: "gone"}},
		entries: map[api.MemoryID]api.MemoryEntry{
			"n1": {ID: "n1", Title: "Note one", Text: "note body", Kind: api.MemoryKindNote},
			// "gone" is a hit but absent from entries: Get fails and it is skipped.
		},
	}
	svc := newService(p)
	res, err := svc.Context(context.Background(), api.MemoryContextParams{WorkspaceID: "ws", Query: "thing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Included) != 2 || res.Included[0] != "s1" || res.Included[1] != "n1" {
		t.Errorf("included = %v, want [s1 n1] (steering before notes, missing note skipped)", res.Included)
	}
	if !strings.Contains(res.Text, "# Note: Note one") || !strings.Contains(res.Text, "note body") {
		t.Errorf("assembled text missing the note:\n%s", res.Text)
	}
}

func TestContextCondensesOverBudgetViaSummarizer(t *testing.T) {
	fs := &fakeSummarizer{reply: "CONDENSED"}
	p := &fakeProvider{steering: []api.MemoryEntry{{ID: "s1", Text: strings.Repeat("x", 500), Kind: api.MemoryKindSteering}}}
	svc := newServiceSum(p, fs)
	res, err := svc.Context(context.Background(), api.MemoryContextParams{WorkspaceID: "ws", Budget: 50})
	if err != nil {
		t.Fatal(err)
	}
	if fs.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", fs.calls)
	}
	if fs.got.Purpose != summarize.PurposeMemory || fs.got.TargetChars != 50 {
		t.Errorf("summarize request = %+v, want memory purpose + budget 50", fs.got)
	}
	if !res.Summarized || res.Text != "CONDENSED" {
		t.Errorf("got %+v, want condensed output", res)
	}
}

func TestContextFallbackTruncationReportsSummarized(t *testing.T) {
	// The default install has no configured backend, so over-budget context is
	// truncated by the fallback. That is a reduction, so Summarized must be true —
	// a caller must be able to tell a digest from the full context.
	p := &fakeProvider{steering: []api.MemoryEntry{
		{ID: "s1", Text: strings.Repeat("rule ", 300), Kind: api.MemoryKindSteering},
	}}
	svc := newService(p) // nil summarizer -> deterministic fallback
	res, err := svc.Context(context.Background(), api.MemoryContextParams{WorkspaceID: "ws", Budget: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summarized {
		t.Error("fallback-truncated context must report Summarized=true, not false")
	}
	if n := len([]rune(res.Text)); n > 100 {
		t.Errorf("truncated text is %d runes, over budget 100", n)
	}
}

func TestContextEmptyWhenNothingApplies(t *testing.T) {
	svc := newService(&fakeProvider{})
	res, err := svc.Context(context.Background(), api.MemoryContextParams{WorkspaceID: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "" || len(res.Included) != 0 || res.Summarized {
		t.Errorf("empty context should be blank, got %+v", res)
	}
}
