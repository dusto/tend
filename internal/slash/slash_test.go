package slash

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// codeOf returns the JSON-RPC code of err, failing if it is not an *rpc.Error.
func codeOf(t *testing.T, err error) int {
	t.Helper()
	var rerr *rpc.Error
	if !errors.As(err, &rerr) {
		t.Fatalf("error %v is not an *rpc.Error", err)
	}
	return rerr.Code
}

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
// action it was asked to perform.
type fakeTasks struct {
	tasks     []api.Task
	gotStatus string
	// last action recorded for invoke assertions.
	action   string
	gotID    string
	gotText  string
	returned api.Task
	err      error
}

func (f *fakeTasks) List(_ context.Context, _ api.WorkspaceID, status string) ([]api.Task, error) {
	f.gotStatus = status
	return f.tasks, f.err
}

func (f *fakeTasks) Show(_ context.Context, _ api.WorkspaceID, id string) (api.Task, error) {
	f.action, f.gotID = "show", id
	return f.returned, f.err
}

func (f *fakeTasks) Claim(_ context.Context, _ api.WorkspaceID, id, _ string) (api.Task, error) {
	f.action, f.gotID = "claim", id
	return f.returned, f.err
}

func (f *fakeTasks) Comment(_ context.Context, _ api.WorkspaceID, id, _, text string) (api.Task, error) {
	f.action, f.gotID, f.gotText = "comment", id, text
	return f.returned, f.err
}

func (f *fakeTasks) CloseTask(_ context.Context, _ api.WorkspaceID, id string) (api.Task, error) {
	f.action, f.gotID = "close", id
	return f.returned, f.err
}

// fakePrompter records the prompt it was asked to forward.
type fakePrompter struct {
	gotSession api.SessionID
	gotText    string
	stop       string
	err        error
}

func (p *fakePrompter) Prompt(_ context.Context, params api.AgentPromptParams) (api.AgentPromptResult, error) {
	p.gotSession, p.gotText = params.SessionID, params.Text
	if p.err != nil {
		return api.AgentPromptResult{}, p.err
	}
	return api.AgentPromptResult{StopReason: p.stop}, nil
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
	svc := NewService(reg, nil, nil, &capture{})

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
	svc := NewService(reg, nil, nil, pub)
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
	svc := NewService(reg, nil, nil, &capture{})
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
	svc := NewService(reg, nil, nil, &capture{})
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
	svc := NewService(reg, nil, nil, pub)

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
	svc := NewService(reg, ft, nil, &capture{})

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
	svc := NewService(reg, ft, nil, &capture{})

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
	svc := NewService(reg, ft, nil, &capture{})

	res, _ := svc.Complete(context.Background(), api.SlashCompleteParams{SessionID: "s1", Command: "tasks", Prefix: "in"})
	if got := candidateValues(res.Candidates); !slices.Equal(got, []string{"in_progress"}) {
		t.Errorf("candidates = %v, want [in_progress]", got)
	}
}

func TestCompleteNoCandidatesForProviderOrUnknown(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	svc := NewService(reg, &fakeTasks{tasks: []api.Task{task("t1", "x")}}, nil, &capture{})
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
	svc := NewService(reg, &fakeTasks{tasks: []api.Task{task("t1", "x")}}, nil, &capture{})

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
	svc := NewService(reg, &fakeTasks{tasks: []api.Task{task("t1", "x")}}, nil, &capture{})

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
	svc := NewService(reg, &fakeTasks{tasks: many}, nil, &capture{})

	res, _ := svc.Complete(context.Background(), api.SlashCompleteParams{SessionID: "s1", Command: "task"})
	if len(res.Candidates) != maxCandidates {
		t.Errorf("candidates = %d, want capped at %d", len(res.Candidates), maxCandidates)
	}
}

func TestInvokeDaemonClaim(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	ft := &fakeTasks{returned: task("tend-9", "claimed one")}
	svc := NewService(reg, ft, &fakePrompter{}, &capture{})

	res, err := svc.Invoke(context.Background(), api.SlashInvokeParams{SessionID: "s1", Command: "claim", Args: "tend-9"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if ft.action != "claim" || ft.gotID != "tend-9" {
		t.Errorf("task action = %q/%q, want claim/tend-9", ft.action, ft.gotID)
	}
	if res.Origin != api.SlashOriginDaemon || res.Message != "Claimed tend-9" || res.Task == nil || res.Task.Title != "claimed one" {
		t.Errorf("result = %+v, want daemon/Claimed tend-9/task", res)
	}
}

func TestInvokeDaemonTasksList(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	ft := &fakeTasks{tasks: []api.Task{task("a", "one"), task("b", "two")}}
	svc := NewService(reg, ft, &fakePrompter{}, &capture{})

	res, err := svc.Invoke(context.Background(), api.SlashInvokeParams{SessionID: "s1", Command: "tasks", Args: "open"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if ft.gotStatus != "open" {
		t.Errorf("status filter = %q, want open", ft.gotStatus)
	}
	if res.Origin != api.SlashOriginDaemon || len(res.Tasks) != 2 || res.Message != "2 task(s)" {
		t.Errorf("result = %+v, want daemon/2 tasks", res)
	}
}

func TestInvokeDaemonCommentSplitsIDAndText(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	ft := &fakeTasks{returned: task("tend-9", "x")}
	svc := NewService(reg, ft, &fakePrompter{}, &capture{})

	_, err := svc.Invoke(context.Background(), api.SlashInvokeParams{SessionID: "s1", Command: "comment", Args: "tend-9 looks good to me"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if ft.action != "comment" || ft.gotID != "tend-9" || ft.gotText != "looks good to me" {
		t.Errorf("comment = id %q text %q, want tend-9 / 'looks good to me'", ft.gotID, ft.gotText)
	}
}

func TestInvokeDaemonMissingArgsIsInvalid(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	svc := NewService(reg, &fakeTasks{}, &fakePrompter{}, &capture{})
	ctx := context.Background()

	for _, cmd := range []string{"task", "claim", "close"} {
		if _, err := svc.Invoke(ctx, api.SlashInvokeParams{SessionID: "s1", Command: cmd}); codeOf(t, err) != rpc.CodeInvalidParams {
			t.Errorf("Invoke %q without arg: got %v, want invalid params", cmd, err)
		}
	}
	// comment needs both an id and text.
	if _, err := svc.Invoke(ctx, api.SlashInvokeParams{SessionID: "s1", Command: "comment", Args: "tend-9"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("comment without text: got %v, want invalid params", err)
	}
}

func TestInvokeForwardsProviderCommandToAgent(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	fp := &fakePrompter{stop: "end_turn"}
	svc := NewService(reg, &fakeTasks{}, fp, &capture{})

	res, err := svc.Invoke(context.Background(), api.SlashInvokeParams{SessionID: "s1", Command: "review", Args: "src/foo.go"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	// The command is reconstructed as prompt text and sent on the session.
	if fp.gotSession != "s1" || fp.gotText != "/review src/foo.go" {
		t.Errorf("forwarded prompt = %q on %q, want '/review src/foo.go' on s1", fp.gotText, fp.gotSession)
	}
	if res.Origin != api.SlashOriginProvider || res.StopReason != "end_turn" {
		t.Errorf("result = %+v, want provider/end_turn", res)
	}
}

func TestInvokeUnknownCommandForwards(t *testing.T) {
	// A command the daemon does not own and the agent did not advertise is still
	// forwarded — the daemon only special-cases its own commands.
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	fp := &fakePrompter{stop: "end_turn"}
	svc := NewService(reg, &fakeTasks{}, fp, &capture{})

	if _, err := svc.Invoke(context.Background(), api.SlashInvokeParams{SessionID: "s1", Command: "frobnicate"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if fp.gotText != "/frobnicate" {
		t.Errorf("forwarded text = %q, want /frobnicate", fp.gotText)
	}
}

func TestInvokeRejectsEmptyCommandAndUnknownSession(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	svc := NewService(reg, &fakeTasks{}, &fakePrompter{}, &capture{})
	ctx := context.Background()

	if _, err := svc.Invoke(ctx, api.SlashInvokeParams{SessionID: "s1", Command: ""}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("empty command: got %v, want invalid params", err)
	}
	if _, err := svc.Invoke(ctx, api.SlashInvokeParams{SessionID: "ghost", Command: "claim", Args: "x"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("unknown session: got %v, want invalid params", err)
	}
}

func TestInvokeDaemonPropagatesTaskError(t *testing.T) {
	reg := session.NewRegistry()
	newSession(t, reg, "s1")
	ft := &fakeTasks{err: &rpc.Error{Code: rpc.CodeInvalidParams, Message: "no such task"}}
	svc := NewService(reg, ft, &fakePrompter{}, &capture{})

	if _, err := svc.Invoke(context.Background(), api.SlashInvokeParams{SessionID: "s1", Command: "close", Args: "nope"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Fatalf("Invoke with failing task action: got %v, want the propagated error", err)
	}
}
