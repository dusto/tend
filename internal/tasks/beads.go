package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dusto/tend/api"
)

// Beads is a Provider backed by the beads CLI (bd) as a subprocess, bound to one
// workspace. Commands run in the workspace's repo directory, where bd discovers
// its .beads database. The adapter emits a change event after each mutation it
// performs (it does not observe out-of-band bd changes). It is safe for
// concurrent use.
type Beads struct {
	name   string
	ws     api.WorkspaceID
	dir    string   // working directory to run bd in (the repo)
	labels []string // label subset this source is scoped to (nil is the whole dir)
	bin    string   // bd executable
	env    []string // extra environment for bd (nil inherits the daemon's)
	// run executes bd with args and returns stdout; the field is the seam tests
	// replace with a fake bd.
	run func(ctx context.Context, args ...string) ([]byte, error)

	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewBeads returns a beads-backed Provider named name for workspace ws, running
// bd in dir. name is the source identity carried in TaskRef.Provider (so several
// beads backlogs are distinguishable); an empty name defaults to "beads". labels
// scope the source to a subset of dir's backlog: List filters to tasks carrying
// all of them and Create tags new tasks with them, so one planning repo can back
// several code repos that each see only their slice.
func NewBeads(name string, ws api.WorkspaceID, dir string, labels ...string) *Beads {
	if name == "" {
		name = "beads"
	}
	b := &Beads{name: name, ws: ws, dir: dir, labels: labels, bin: "bd", subs: make(map[chan Event]struct{})}
	b.run = b.exec
	return b
}

// Name returns the provider identifier carried in TaskRef.Provider.
func (b *Beads) Name() string { return b.name }

// SetExec overrides the bd executable and its environment. It is for tests that
// run a fake bd (for example a re-exec of the test binary), so the fake-mode env
// reaches the child without polluting the daemon's own environment.
func (b *Beads) SetExec(bin string, env []string) {
	b.bin = bin
	b.env = env
}

// exec runs bd in the provider's directory and returns its stdout.
func (b *Beads) exec(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, b.bin, args...)
	cmd.Dir = b.dir
	if len(b.env) > 0 {
		cmd.Env = append(os.Environ(), b.env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("beads: bd %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Create creates an issue and returns it.
func (b *Beads) Create(ctx context.Context, p CreateParams) (Task, error) {
	// Tag new tasks with the source's labels too, so a task written through a
	// filtered source lands in that source's view.
	labels := unionLabels(p.Labels, b.labels)
	args := []string{"create", p.Title, "--json"}
	if p.Description != "" {
		args = append(args, "-d", p.Description)
	}
	if len(labels) > 0 {
		args = append(args, "-l", strings.Join(labels, ","))
	}
	out, err := b.run(ctx, args...)
	if err != nil {
		return Task{}, err
	}
	var iss bdIssue
	if err := json.Unmarshal(out, &iss); err != nil {
		return Task{}, fmt.Errorf("beads: parsing create output: %w", err)
	}
	t := b.toTask(iss)
	if len(t.Labels) == 0 {
		t.Labels = labels // bd create output may omit labels it was given
	}
	b.publish(Event{Ref: t.Ref, Kind: EventCreated})
	return t, nil
}

// Show returns the issue for ref, including its comments.
func (b *Beads) Show(ctx context.Context, ref api.TaskRef) (Task, error) {
	if err := b.checkRef(ref); err != nil {
		return Task{}, err
	}
	out, err := b.run(ctx, "show", ref.ID, "--long", "--json")
	if err != nil {
		return Task{}, err
	}
	issues, err := decodeIssues(out)
	if err != nil {
		return Task{}, err
	}
	if len(issues) == 0 {
		return Task{}, fmt.Errorf("beads: no such task %q", ref.ID)
	}
	return b.toTask(issues[0]), nil
}

// List returns the issues matching f.
func (b *Beads) List(ctx context.Context, f Filter) ([]Task, error) {
	args := []string{"list", "--json"}
	if f.Status != "" {
		args = append(args, "--status", f.Status)
	}
	// Scope to the source's labels (bd -l is AND: a task must carry all of them).
	if len(b.labels) > 0 {
		args = append(args, "-l", strings.Join(b.labels, ","))
	}
	out, err := b.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	issues, err := decodeIssues(out)
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(issues))
	for _, iss := range issues {
		tasks = append(tasks, b.toTask(iss))
	}
	return tasks, nil
}

// Claim assigns the issue and marks it in progress.
func (b *Beads) Claim(ctx context.Context, ref api.TaskRef, assignee string) error {
	if err := b.checkRef(ref); err != nil {
		return err
	}
	if _, err := b.run(ctx, "update", ref.ID, "--assignee", assignee, "--status", StatusInProgress); err != nil {
		return err
	}
	b.publish(Event{Ref: ref, Kind: EventUpdated})
	return nil
}

// Comment appends a comment to the issue.
func (b *Beads) Comment(ctx context.Context, ref api.TaskRef, c Comment) error {
	if err := b.checkRef(ref); err != nil {
		return err
	}
	args := []string{"comment", ref.ID, c.Text}
	if c.Author != "" {
		args = append(args, "--actor", c.Author)
	}
	if _, err := b.run(ctx, args...); err != nil {
		return err
	}
	b.publish(Event{Ref: ref, Kind: EventCommented})
	return nil
}

// Close closes the issue.
func (b *Beads) Close(ctx context.Context, ref api.TaskRef) error {
	if err := b.checkRef(ref); err != nil {
		return err
	}
	if _, err := b.run(ctx, "close", ref.ID); err != nil {
		return err
	}
	b.publish(Event{Ref: ref, Kind: EventClosed})
	return nil
}

// Link records a dependency between two issues.
func (b *Beads) Link(ctx context.Context, from, to api.TaskRef, kind LinkType) error {
	if err := b.checkRef(from); err != nil {
		return err
	}
	if err := b.checkRef(to); err != nil {
		return err
	}
	args := []string{"link", from.ID, to.ID}
	if t, ok := bdLinkType(kind); ok {
		args = append(args, "--type", t)
	}
	if _, err := b.run(ctx, args...); err != nil {
		return err
	}
	b.publish(Event{Ref: from, Kind: EventUpdated})
	return nil
}

// Events returns a channel of change events for mutations this provider performs;
// it closes when ctx is done.
func (b *Beads) Events(ctx context.Context) (<-chan Event, error) {
	ch := make(chan Event, eventBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, ch)
		close(ch)
		b.mu.Unlock()
	}()
	return ch, nil
}

func (b *Beads) publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (b *Beads) ref(id string) api.TaskRef {
	return api.TaskRef{Provider: b.name, WorkspaceID: b.ws, ID: id}
}

// checkRef rejects a ref that does not belong to this provider+workspace, so a
// forged or stale ref cannot reach another workspace's tasks.
func (b *Beads) checkRef(ref api.TaskRef) error {
	if ref.Provider != b.name || ref.WorkspaceID != b.ws {
		return fmt.Errorf("tasks: ref %s/%s does not belong to %s/%s", ref.Provider, ref.WorkspaceID, b.name, b.ws)
	}
	return nil
}

func (b *Beads) toTask(i bdIssue) Task {
	t := Task{
		Ref:         b.ref(i.ID),
		Title:       i.Title,
		Status:      i.Status,
		Description: i.Description,
		Assignee:    i.Assignee,
		Labels:      i.Labels,
	}
	for _, c := range i.Comments {
		t.Comments = append(t.Comments, Comment{Author: c.Author, Text: c.Text, At: c.CreatedAt})
	}
	return t
}

// unionLabels returns requested followed by any of extra not already present,
// preserving order and dropping duplicates.
func unionLabels(requested, extra []string) []string {
	if len(extra) == 0 {
		return requested
	}
	seen := make(map[string]struct{}, len(requested)+len(extra))
	out := make([]string, 0, len(requested)+len(extra))
	for _, l := range requested {
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	for _, l := range extra {
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	return out
}

// bdLinkType maps a LinkType to bd's --type value, and whether to pass --type
// (bd's default link is depends-on: `bd link from to` makes to block from).
func bdLinkType(k LinkType) (string, bool) {
	switch k {
	case LinkParent:
		return "parent-child", true
	case LinkRelated:
		return "related", true
	default: // LinkDependsOn is bd's default
		return "", false
	}
}

// bdIssue is the subset of bd's JSON issue the adapter reads.
type bdIssue struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Status      string      `json:"status"`
	Assignee    string      `json:"assignee"`
	Labels      []string    `json:"labels"`
	Comments    []bdComment `json:"comments"`
}

type bdComment struct {
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// decodeIssues parses a bd JSON array of issues (show/list output).
func decodeIssues(out []byte) ([]bdIssue, error) {
	var issues []bdIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("beads: parsing bd output: %w", err)
	}
	return issues, nil
}
