package tasks

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

// recordingEmitter captures published events for assertions.
type recordingEmitter struct {
	mu     sync.Mutex
	events []api.Event
}

func (e *recordingEmitter) Publish(ev api.Event) (api.Event, error) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
	return ev, nil
}

// typesFor returns the event types emitted on a stream, in order.
func (e *recordingEmitter) typesFor(stream api.StreamID) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		if ev.StreamID == stream {
			out = append(out, ev.Type)
		}
	}
	return out
}

func newService(t *testing.T) (*Service, *recordingEmitter) {
	t.Helper()
	emit := &recordingEmitter{}
	s := NewService(func(ws api.WorkspaceID) Provider { return NewFake(ws) }, emit)
	t.Cleanup(s.Close)
	return s, emit
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestServiceCreateShowList(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()

	created, err := s.create(ctx, api.TaskCreateParams{WorkspaceID: "ws1", Title: "fix", Description: "d", Labels: []string{"m0"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Ref.Provider != "fake" || created.Ref.WorkspaceID != "ws1" || created.Status != StatusOpen || created.Title != "fix" {
		t.Fatalf("created = %+v", created)
	}

	got, err := s.show(ctx, api.TaskShowParams{Ref: created.Ref})
	if err != nil || got.Description != "d" || len(got.Labels) != 1 {
		t.Fatalf("show = %+v, err = %v", got, err)
	}

	list, err := s.list(ctx, api.TaskListParams{WorkspaceID: "ws1"})
	if err != nil || len(list.Tasks) != 1 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	// Status filter that matches nothing.
	closed, _ := s.list(ctx, api.TaskListParams{WorkspaceID: "ws1", Status: StatusClosed})
	if len(closed.Tasks) != 0 {
		t.Errorf("closed list = %d, want 0", len(closed.Tasks))
	}
}

func TestServiceCreateShowRichFields(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()

	created, err := s.create(ctx, api.TaskCreateParams{
		WorkspaceID:        "ws1",
		Title:              "fix",
		AcceptanceCriteria: "given X, when Y, then Z",
		Priority:           "1",
		Design:             "sketch of the approach",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The create path echoes the authoring fields back.
	if created.AcceptanceCriteria != "given X, when Y, then Z" || created.Priority != "1" || created.Design != "sketch of the approach" {
		t.Fatalf("created = %+v", created)
	}
	// And they round-trip through show.
	got, err := s.show(ctx, api.TaskShowParams{Ref: created.Ref})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if got.AcceptanceCriteria != created.AcceptanceCriteria || got.Priority != created.Priority || got.Design != created.Design {
		t.Fatalf("show = %+v, want rich fields preserved", got)
	}
}

func TestServiceClaimCommentClose(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	created, _ := s.create(ctx, api.TaskCreateParams{WorkspaceID: "ws1", Title: "x"})

	claimed, err := s.claim(ctx, api.TaskClaimParams{Ref: created.Ref, Assignee: "agent-1"})
	if err != nil || claimed.Status != StatusInProgress || claimed.Assignee != "agent-1" {
		t.Fatalf("claim = %+v, err = %v", claimed, err)
	}
	commented, err := s.comment(ctx, api.TaskCommentParams{Ref: created.Ref, Text: "working", Author: "agent-1"})
	if err != nil || len(commented.Comments) != 1 || commented.Comments[0].Text != "working" {
		t.Fatalf("comment = %+v, err = %v", commented, err)
	}
	closed, err := s.close(ctx, api.TaskCloseParams{Ref: created.Ref})
	if err != nil || closed.Status != StatusClosed {
		t.Fatalf("close = %+v, err = %v", closed, err)
	}
}

func TestServiceUnknownRef(t *testing.T) {
	s, _ := newService(t)
	_, err := s.show(context.Background(), api.TaskShowParams{Ref: api.TaskRef{Provider: "fake", WorkspaceID: "ws1", ID: "nope"}})
	if err == nil {
		t.Error("show of unknown task should error")
	}
}

func TestServiceBridgesEventsToWorkspaceStream(t *testing.T) {
	s, emit := newService(t)
	ctx := context.Background()
	stream := api.WorkspaceStream("ws1")

	created, _ := s.create(ctx, api.TaskCreateParams{WorkspaceID: "ws1", Title: "x"})
	_, _ = s.claim(ctx, api.TaskClaimParams{Ref: created.Ref, Assignee: "a"})
	_, _ = s.comment(ctx, api.TaskCommentParams{Ref: created.Ref, Text: "c"})
	_, _ = s.close(ctx, api.TaskCloseParams{Ref: created.Ref})

	want := []string{"task_created", "task_updated", "task_commented", "task_closed"}
	waitFor(t, func() bool { return len(emit.typesFor(stream)) >= len(want) })

	got := emit.typesFor(stream)
	if len(got) != len(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event types = %v, want %v", got, want)
		}
	}

	// The payload carries the task ref so a client can fetch full state.
	emit.mu.Lock()
	defer emit.mu.Unlock()
	for _, ev := range emit.events {
		if ev.StreamID != stream {
			continue
		}
		if ev.Scope != api.ScopeWorkspace {
			t.Errorf("event %s scope = %q, want workspace", ev.Type, ev.Scope)
		}
		var tc api.TaskChange
		if err := json.Unmarshal(ev.Payload, &tc); err != nil || tc.Ref.ID != created.Ref.ID {
			t.Errorf("payload = %s, err = %v", ev.Payload, err)
		}
	}
}

func TestServiceProviderReusedPerWorkspace(t *testing.T) {
	s, _ := newService(t)
	a := s.provider("ws1")
	b := s.provider("ws1")
	if a != b {
		t.Error("provider should be reused for the same workspace")
	}
	if c := s.provider("ws2"); c == a {
		t.Error("different workspaces should get different providers")
	}
}
