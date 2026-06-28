package slash

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/session"
)

// capture is a Publisher that records the events it receives.
type capture struct {
	mu sync.Mutex
	ev []api.Event
}

func (c *capture) Publish(ev api.Event) (api.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ev = append(c.ev, ev)
	return ev, nil
}

func (c *capture) events() []api.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]api.Event(nil), c.ev...)
}

func newSession(t *testing.T, reg *session.Registry, id api.SessionID) {
	t.Helper()
	reg.Create(id, "codex", "ws1", api.TaskRef{}, "/wt")
}

// fakeTasks is a Tasks that returns a fixed set for any workspace and records the
// status filter it was asked for.
type fakeTasks struct {
	tasks     []api.Task
	gotStatus string
}

func (f *fakeTasks) List(_ context.Context, _ api.WorkspaceID, status string) ([]api.Task, error) {
	f.gotStatus = status
	return f.tasks, nil
}

func task(id, title string) api.Task {
	return api.Task{Ref: api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: id}, Title: title}
}

func candidateValues(cs []api.SlashCandidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Value
	}
	return out
}

func names(cmds []api.SlashCommand) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Name
	}
	return out
}

func TestListDaemonCommandsOnly(t *testing.T) {
	reg := session.NewRegistry()
	svc := NewService(reg, nil, &capture{})

	// No session and no provider commands: the daemon commands are still offered.
	res, err := svc.List(context.Background(), api.SlashListParams{SessionID: "missing"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Commands) != len(daemonCommands()) {
		t.Fatalf("got %d commands, want %d daemon commands", len(res.Commands), len(daemonCommands()))
	}
	for _, c := range res.Commands {
		if c.Origin != api.SlashOriginDaemon {
			t.Errorf("command %q origin = %q, want daemon", c.Name, c.Origin)
		}
	}
}

func TestSetSessionCommandsMergesAndEmits(t *testing.T) {
	reg := session.NewRegistry()
	pub := &capture{}
	svc := NewService(reg, nil, pub)
	newSession(t, reg, "s1")

	provider := []api.SlashCommand{
		{Name: "review", Description: "review the diff", Origin: api.SlashOriginProvider, ArgHint: "<path>"},
		{Name: "compact", Origin: api.SlashOriginProvider},
	}
	svc.SetSessionCommands("s1", provider)

	// The merged set is daemon commands first, then the session's provider ones.
	want := append(names(daemonCommands()), "review", "compact")
	res, _ := svc.List(context.Background(), api.SlashListParams{SessionID: "s1"})
	if got := names(res.Commands); !slices.Equal(got, want) {
		t.Errorf("merged = %v, want %v", got, want)
	}

	// The change is emitted as slash_commands_updated carrying the same merged set.
	evs := pub.events()
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "slash_commands_updated" || ev.StreamID != api.SessionStream("s1") || ev.Scope != api.ScopeSession {
		t.Fatalf("event = %+v", ev)
	}
	var p api.SlashCommandsUpdated
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s1" || !slices.Equal(names(p.Commands), want) {
		t.Errorf("payload = %+v", p)
	}
}

func TestSetSessionCommandsReplacesPriorSet(t *testing.T) {
	reg := session.NewRegistry()
	svc := NewService(reg, nil, &capture{})
	newSession(t, reg, "s1")

	svc.SetSessionCommands("s1", []api.SlashCommand{{Name: "old", Origin: api.SlashOriginProvider}})
	svc.SetSessionCommands("s1", []api.SlashCommand{{Name: "new", Origin: api.SlashOriginProvider}})

	res, _ := svc.List(context.Background(), api.SlashListParams{SessionID: "s1"})
	got := names(res.Commands)
	if slices.Contains(got, "old") || !slices.Contains(got, "new") {
		t.Errorf("merged = %v, want the prior 'old' replaced by 'new'", got)
	}
}

func TestProviderCommandsCannotShadowDaemonCommands(t *testing.T) {
	reg := session.NewRegistry()
	svc := NewService(reg, nil, &capture{})
	newSession(t, reg, "s1")

	// The provider advertises a name the daemon owns ("close") plus a fresh one.
	svc.SetSessionCommands("s1", []api.SlashCommand{
		{Name: "close", Description: "provider close", Origin: api.SlashOriginProvider},
		{Name: "review", Origin: api.SlashOriginProvider},
	})

	res, _ := svc.List(context.Background(), api.SlashListParams{SessionID: "s1"})
	// "close" appears exactly once, as the daemon command; the provider duplicate
	// is dropped. "review" (no collision) is kept.
	closeCount := 0
	for _, c := range res.Commands {
		if c.Name == "close" {
			closeCount++
			if c.Origin != api.SlashOriginDaemon {
				t.Errorf("close origin = %q, want daemon (authoritative)", c.Origin)
			}
		}
	}
	if closeCount != 1 {
		t.Errorf("close appears %d times, want 1 (no provider shadow)", closeCount)
	}
	if !slices.Contains(names(res.Commands), "review") {
		t.Errorf("non-colliding provider command 'review' was dropped: %v", names(res.Commands))
	}
}

func TestSetSessionCommandsUnknownSessionNoOp(t *testing.T) {
	reg := session.NewRegistry()
	pub := &capture{}
	svc := NewService(reg, nil, pub)

	// No session created: storing must not panic and must not emit (nothing to
	// attribute the merged set to).
	svc.SetSessionCommands("ghost", []api.SlashCommand{{Name: "x", Origin: api.SlashOriginProvider}})
	if len(pub.events()) != 0 {
		t.Errorf("emitted for an unknown session: %v", pub.events())
	}
}

func TestCompleteTaskIDsByPrefix(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	ft := &fakeTasks{tasks: []api.Task{
		task("tend-e7p.14", "slash commands"),
		task("tend-e7p.15", "something else"),
		task("tend-48d.22", "switchers"),
	}}
	svc := NewService(reg, ft, &capture{})

	res, err := svc.Complete(context.Background(), api.SlashCompleteParams{
		SessionID: "s1", Command: "claim", Prefix: "tend-e7p",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := candidateValues(res.Candidates); !slices.Equal(got, []string{"tend-e7p.14", "tend-e7p.15"}) {
		t.Errorf("candidates = %v, want the two tend-e7p ids", got)
	}
	// The task title rides along as the candidate detail.
	if res.Candidates[0].Detail != "slash commands" {
		t.Errorf("detail = %q, want the task title", res.Candidates[0].Detail)
	}
}

func TestCompleteTaskIDsCaseInsensitiveAndEmptyPrefix(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	ft := &fakeTasks{tasks: []api.Task{task("ABC-1", "one"), task("xyz-2", "two")}}
	svc := NewService(reg, ft, &capture{})

	// Empty prefix lists everything.
	all, _ := svc.Complete(context.Background(), api.SlashCompleteParams{SessionID: "s1", Command: "task"})
	if len(all.Candidates) != 2 {
		t.Errorf("empty-prefix candidates = %v, want both", candidateValues(all.Candidates))
	}
	// Prefix match is case-insensitive.
	res, _ := svc.Complete(context.Background(), api.SlashCompleteParams{SessionID: "s1", Command: "task", Prefix: "abc"})
	if got := candidateValues(res.Candidates); !slices.Equal(got, []string{"ABC-1"}) {
		t.Errorf("candidates = %v, want [ABC-1]", got)
	}
}

func TestCompleteStatusForTasksCommand(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	// /tasks completes a status, not a task id — the task lister is not consulted.
	ft := &fakeTasks{tasks: []api.Task{task("t1", "x")}}
	svc := NewService(reg, ft, &capture{})

	res, _ := svc.Complete(context.Background(), api.SlashCompleteParams{SessionID: "s1", Command: "tasks", Prefix: "in"})
	if got := candidateValues(res.Candidates); !slices.Equal(got, []string{"in_progress"}) {
		t.Errorf("candidates = %v, want [in_progress]", got)
	}
}

func TestCompleteNoCandidatesForProviderOrUnknown(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	svc := NewService(reg, &fakeTasks{tasks: []api.Task{task("t1", "x")}}, &capture{})
	ctx := context.Background()

	for _, cmd := range []string{"review" /* provider */, "frobnicate" /* unknown */, "" /* none */} {
		res, err := svc.Complete(ctx, api.SlashCompleteParams{SessionID: "s1", Command: cmd, Prefix: "t"})
		if err != nil {
			t.Fatalf("Complete(%q): %v", cmd, err)
		}
		if len(res.Candidates) != 0 {
			t.Errorf("Complete(%q) = %v, want no candidates", cmd, candidateValues(res.Candidates))
		}
	}
}

func TestCompleteEmptyResultMarshalsAsArray(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	svc := NewService(reg, &fakeTasks{tasks: []api.Task{task("t1", "x")}}, &capture{})

	// The result schema requires candidates to be an array; a no-match response
	// must marshal as [] not null. Covers the no-command (default) path.
	res, _ := svc.Complete(context.Background(), api.SlashCompleteParams{SessionID: "s1", Command: "review"})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"candidates":[]}` {
		t.Errorf("marshaled empty result = %s, want {\"candidates\":[]}", raw)
	}
}

func TestCompleteUnknownSessionIsEmpty(t *testing.T) {
	reg := session.NewRegistry()
	svc := NewService(reg, &fakeTasks{tasks: []api.Task{task("t1", "x")}}, &capture{})

	res, err := svc.Complete(context.Background(), api.SlashCompleteParams{SessionID: "ghost", Command: "claim", Prefix: "t"})
	if err != nil || len(res.Candidates) != 0 {
		t.Errorf("Complete unknown session = (%v, %v), want (nil, no candidates)", res.Candidates, err)
	}
}

func TestCompleteCapsCandidates(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	many := make([]api.Task, 0, maxCandidates+10)
	for i := range maxCandidates + 10 {
		many = append(many, task("t"+string(rune('a'+i%26))+string(rune('0'+i/26)), ""))
	}
	svc := NewService(reg, &fakeTasks{tasks: many}, &capture{})

	res, _ := svc.Complete(context.Background(), api.SlashCompleteParams{SessionID: "s1", Command: "task"})
	if len(res.Candidates) != maxCandidates {
		t.Errorf("candidates = %d, want capped at %d", len(res.Candidates), maxCandidates)
	}
}
