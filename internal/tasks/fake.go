package tasks

import (
	"context"
	"fmt"
	"sync"

	"github.com/dusto/tend/api"
)

// eventBuffer is the per-subscriber channel capacity for Fake.Events.
const eventBuffer = 64

// Fake is an in-memory Provider for tests and local development. It is bound to
// one workspace and is safe for concurrent use.
type Fake struct {
	name string

	mu    sync.Mutex
	ws    api.WorkspaceID
	seq   int
	tasks map[string]*Task
	links []link
	subs  map[chan Event]struct{}
}

type link struct {
	from, to api.TaskRef
	kind     LinkType
}

// NewFake returns a Fake provider for the given workspace.
func NewFake(ws api.WorkspaceID) *Fake {
	return &Fake{
		name:  "fake",
		ws:    ws,
		tasks: make(map[string]*Task),
		subs:  make(map[chan Event]struct{}),
	}
}

// Name returns the provider identifier.
func (f *Fake) Name() string { return f.name }

func (f *Fake) ref(id string) api.TaskRef {
	return api.TaskRef{Provider: f.name, WorkspaceID: f.ws, ID: id}
}

// Create adds a new open task.
func (f *Fake) Create(_ context.Context, p CreateParams) (Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("t%d", f.seq)
	t := &Task{
		Ref:         f.ref(id),
		Title:       p.Title,
		Status:      StatusOpen,
		Description: p.Description,
		Labels:      append([]string(nil), p.Labels...),
	}
	f.tasks[id] = t
	f.publish(Event{Ref: t.Ref, Kind: EventCreated})
	return clone(t), nil
}

// Show returns the task for ref.
func (f *Fake) Show(_ context.Context, ref api.TaskRef) (Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.lookup(ref)
	if err != nil {
		return Task{}, err
	}
	return clone(t), nil
}

// List returns the tasks matching f.
func (f *Fake) List(_ context.Context, filter Filter) ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Task
	for _, t := range f.tasks {
		if filter.Status == "" || t.Status == filter.Status {
			out = append(out, clone(t))
		}
	}
	return out, nil
}

// Claim assigns the task and marks it in progress.
func (f *Fake) Claim(_ context.Context, ref api.TaskRef, assignee string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.lookup(ref)
	if err != nil {
		return err
	}
	t.Assignee = assignee
	t.Status = StatusInProgress
	f.publish(Event{Ref: t.Ref, Kind: EventUpdated})
	return nil
}

// Comment appends a comment to the task.
func (f *Fake) Comment(_ context.Context, ref api.TaskRef, c Comment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.lookup(ref)
	if err != nil {
		return err
	}
	t.Comments = append(t.Comments, c)
	f.publish(Event{Ref: t.Ref, Kind: EventCommented})
	return nil
}

// Close marks the task closed.
func (f *Fake) Close(_ context.Context, ref api.TaskRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.lookup(ref)
	if err != nil {
		return err
	}
	t.Status = StatusClosed
	f.publish(Event{Ref: t.Ref, Kind: EventClosed})
	return nil
}

// Link records a relationship between two tasks.
func (f *Fake) Link(_ context.Context, from, to api.TaskRef, kind LinkType) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.lookup(from); err != nil {
		return err
	}
	if _, err := f.lookup(to); err != nil {
		return err
	}
	f.links = append(f.links, link{from: from, to: to, kind: kind})
	f.publish(Event{Ref: from, Kind: EventUpdated})
	return nil
}

// Events returns a channel of task changes that closes when ctx is done.
func (f *Fake) Events(ctx context.Context) (<-chan Event, error) {
	ch := make(chan Event, eventBuffer)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()

	go func() {
		<-ctx.Done()
		f.mu.Lock()
		delete(f.subs, ch)
		close(ch)
		f.mu.Unlock()
	}()
	return ch, nil
}

// lookup returns the task for ref; callers hold f.mu. It rejects a ref that does
// not belong to this provider+workspace, so a forged or stale ref cannot reach
// another workspace's task.
func (f *Fake) lookup(ref api.TaskRef) (*Task, error) {
	if ref.Provider != f.name || ref.WorkspaceID != f.ws {
		return nil, fmt.Errorf("tasks: ref %s/%s does not belong to %s/%s", ref.Provider, ref.WorkspaceID, f.name, f.ws)
	}
	if t, ok := f.tasks[ref.ID]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("tasks: no such task %q", ref.ID)
}

// clone returns a deep copy of t so callers cannot mutate the fake's state
// through the slices in a returned Task.
func clone(t *Task) Task {
	c := *t
	if t.Labels != nil {
		c.Labels = append([]string(nil), t.Labels...)
	}
	if t.Comments != nil {
		c.Comments = append([]Comment(nil), t.Comments...)
	}
	return c
}

// publish delivers ev to current subscribers; callers hold f.mu. Delivery is
// best-effort (a subscriber that is not draining may miss events under load).
func (f *Fake) publish(ev Event) {
	for ch := range f.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
