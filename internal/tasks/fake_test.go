package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

// Fake must satisfy the Provider interface.
var _ Provider = (*Fake)(nil)

func TestCreateShowList(t *testing.T) {
	f := NewFake("ws1")
	ctx := context.Background()

	created, err := f.Create(ctx, CreateParams{Title: "fix bug", Description: "details", Labels: []string{"m0"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Ref.Provider != "fake" || created.Ref.WorkspaceID != "ws1" || created.Ref.ID == "" {
		t.Fatalf("ref = %+v", created.Ref)
	}
	if created.Status != StatusOpen || created.Title != "fix bug" {
		t.Errorf("created = %+v", created)
	}

	got, err := f.Show(ctx, created.Ref)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.Description != "details" {
		t.Errorf("show desc = %q", got.Description)
	}

	all, _ := f.List(ctx, Filter{})
	if len(all) != 1 {
		t.Fatalf("List len = %d, want 1", len(all))
	}
}

func TestClaimCommentClose(t *testing.T) {
	f := NewFake("ws1")
	ctx := context.Background()
	tk, _ := f.Create(ctx, CreateParams{Title: "x"})

	if err := f.Claim(ctx, tk.Ref, "agent-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := f.Comment(ctx, tk.Ref, Comment{Author: "agent-1", Text: "working", At: time.Now()}); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	got, _ := f.Show(ctx, tk.Ref)
	if got.Status != StatusInProgress || got.Assignee != "agent-1" {
		t.Errorf("after claim: %+v", got)
	}
	if len(got.Comments) != 1 || got.Comments[0].Text != "working" {
		t.Errorf("comments = %+v", got.Comments)
	}

	if err := f.Close(ctx, tk.Ref); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, _ := f.Show(ctx, tk.Ref); got.Status != StatusClosed {
		t.Errorf("status after close = %q", got.Status)
	}

	// Filtering by status.
	open, _ := f.List(ctx, Filter{Status: StatusOpen})
	if len(open) != 0 {
		t.Errorf("open tasks = %d, want 0", len(open))
	}
}

func TestUnknownTaskErrors(t *testing.T) {
	f := NewFake("ws1")
	if _, err := f.Show(context.Background(), api.TaskRef{ID: "nope"}); err == nil {
		t.Error("Show of unknown task should error")
	}
}

func TestLink(t *testing.T) {
	f := NewFake("ws1")
	ctx := context.Background()
	a, _ := f.Create(ctx, CreateParams{Title: "a"})
	b, _ := f.Create(ctx, CreateParams{Title: "b"})

	if err := f.Link(ctx, a.Ref, b.Ref, LinkDependsOn); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if err := f.Link(ctx, a.Ref, api.TaskRef{ID: "missing"}, LinkDependsOn); err == nil {
		t.Error("Link to a missing task should error")
	}
}

func TestEventsStream(t *testing.T) {
	f := NewFake("ws1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := f.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	tk, _ := f.Create(ctx, CreateParams{Title: "x"})
	_ = f.Claim(ctx, tk.Ref, "a")
	_ = f.Close(ctx, tk.Ref)

	want := []EventKind{EventCreated, EventUpdated, EventClosed}
	for _, wantKind := range want {
		select {
		case ev := <-ch:
			if ev.Kind != wantKind || ev.Ref.ID != tk.Ref.ID {
				t.Fatalf("event = %+v, want kind %s", ev, wantKind)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s event", wantKind)
		}
	}

	// Cancelling the context closes the stream.
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			// drain any buffered events until closed
			for range ch {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Events channel not closed after ctx cancel")
	}
}
