// Package tasks defines the provider-agnostic task interface the daemon drives,
// plus an in-memory fake. A task is identified by api.TaskRef; concrete backends
// (such as beads) adapt their CLI/storage to this interface.
package tasks

import (
	"context"
	"time"

	"github.com/dusto/tend/api"
)

// Common task statuses. Statuses are provider-defined strings; these are the
// ones the daemon understands across providers.
const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusClosed     = "closed"
)

// Task is a provider-agnostic view of one task.
type Task struct {
	Ref         api.TaskRef
	Title       string
	Status      string
	Description string
	Assignee    string
	Labels      []string
	Comments    []Comment
}

// Comment is a note attached to a task.
type Comment struct {
	Author string
	Text   string
	At     time.Time
}

// CreateParams describes a task to create.
type CreateParams struct {
	Title       string
	Description string
	Labels      []string
}

// Filter selects tasks for List. A zero Filter matches everything.
type Filter struct {
	Status string // "" matches any status
}

// LinkType is the relationship Link establishes between two tasks.
type LinkType string

// LinkType values.
const (
	LinkDependsOn LinkType = "depends_on"
	LinkParent    LinkType = "parent"
	LinkRelated   LinkType = "related"
)

// EventKind classifies a task change.
type EventKind string

// EventKind values.
const (
	EventCreated   EventKind = "created"
	EventUpdated   EventKind = "updated"
	EventCommented EventKind = "commented"
	EventClosed    EventKind = "closed"
)

// Event reports a change to a task.
type Event struct {
	Ref  api.TaskRef
	Kind EventKind
}

// Provider is the v1 task-provider interface. A Provider instance is bound to
// one workspace; the TaskRefs it returns carry that workspace and the provider's
// Name. All methods are context-aware.
type Provider interface {
	// Name is the provider identifier carried in TaskRef.Provider (e.g. "beads").
	Name() string
	Create(ctx context.Context, p CreateParams) (Task, error)
	Show(ctx context.Context, ref api.TaskRef) (Task, error)
	List(ctx context.Context, f Filter) ([]Task, error)
	// Claim assigns the task to assignee and marks it in progress.
	Claim(ctx context.Context, ref api.TaskRef, assignee string) error
	Comment(ctx context.Context, ref api.TaskRef, c Comment) error
	Close(ctx context.Context, ref api.TaskRef) error
	Link(ctx context.Context, from, to api.TaskRef, kind LinkType) error
	// Events returns a stream of task changes that closes when ctx is done.
	Events(ctx context.Context) (<-chan Event, error)
}
