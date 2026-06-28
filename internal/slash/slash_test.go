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

func names(cmds []api.SlashCommand) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Name
	}
	return out
}

func TestListDaemonCommandsOnly(t *testing.T) {
	reg := session.NewRegistry()
	svc := NewService(reg, &capture{})

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
	svc := NewService(reg, pub)
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
	svc := NewService(reg, &capture{})
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
	svc := NewService(reg, &capture{})
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
	svc := NewService(reg, pub)

	// No session created: storing must not panic and must not emit (nothing to
	// attribute the merged set to).
	svc.SetSessionCommands("ghost", []api.SlashCommand{{Name: "x", Origin: api.SlashOriginProvider}})
	if len(pub.events()) != 0 {
		t.Errorf("emitted for an unknown session: %v", pub.events())
	}
}
