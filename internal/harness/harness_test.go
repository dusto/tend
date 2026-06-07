package harness

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/daemon"
	"github.com/dusto/tend/internal/tasks"
)

// TestMain dispatches the re-exec'd fake modes: when the daemon spawns this test
// binary as a provider (with EnvFakeACP) or as bd (with EnvFakeBd), it acts as
// that fake rather than running the test suite.
func TestMain(m *testing.M) {
	switch {
	case os.Getenv(EnvFakeACP) != "":
		RunFakeACP()
		return
	case os.Getenv(EnvFakeBd) != "":
		RunFakeBd()
		return
	}
	os.Exit(m.Run())
}

// fakeDaemon starts a daemon wired to the fake ACP server (this test binary).
func fakeDaemon(t *testing.T, opts ...daemon.Option) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	opts = append([]daemon.Option{daemon.WithACPConfig(FakeACPConfig(exe))}, opts...)
	srv, sock, err := StartServer(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	return sock
}

func dial(t *testing.T, sock string) *Client {
	t.Helper()
	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mustCall(t *testing.T, c *Client, method string, params, result any) {
	t.Helper()
	cx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Call(cx, method, params, result); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

func TestSmokeAgentTurnStreamsEvents(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)

	var hello api.HelloResult
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &hello)
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"},
		WorktreeRoot: t.TempDir(),
	}, &started)
	if started.SessionID == "" {
		t.Fatalf("start = %+v", started)
	}

	// Subscribe before prompting so the streamed turn is delivered live.
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})

	var prompt api.AgentPromptResult
	mustCall(t, c, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "hi"}, &prompt)
	if prompt.StopReason != "end_turn" {
		t.Errorf("stop reason = %q", prompt.StopReason)
	}

	if !c.WaitEventCount(started.StreamID, 5, 3*time.Second) {
		t.Fatalf("did not receive the turn's events; got %v", c.EventTypes(started.StreamID))
	}
	want := []string{"agent_message_chunk", "agent_message_chunk", "tool_call", "tool_call_update", "turn_end"}
	got := c.EventTypes(started.StreamID)
	if len(got) < len(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event types = %v, want %v", got, want)
		}
	}
}

func TestSmokeTaskRoundTripFakeProvider(t *testing.T) {
	sock := fakeDaemon(t) // default in-memory task provider (stateful)
	c := dial(t, sock)

	var created api.Task
	mustCall(t, c, "task.create", api.TaskCreateParams{WorkspaceID: "ws1", Title: "do it"}, &created)
	if created.Ref.ID == "" {
		t.Fatalf("created = %+v", created)
	}
	var got api.Task
	mustCall(t, c, "task.claim", api.TaskClaimParams{Ref: created.Ref, Assignee: "agent"}, &got)
	if got.Status != tasks.StatusInProgress || got.Assignee != "agent" {
		t.Errorf("claimed = %+v", got)
	}
	var list api.TaskListResult
	mustCall(t, c, "task.list", api.TaskListParams{WorkspaceID: "ws1"}, &list)
	if len(list.Tasks) != 1 {
		t.Errorf("list = %+v", list)
	}
}

func TestSmokeTaskViaFakeBd(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// A beads-backed task provider whose bd is this test binary in fake-bd mode.
	factory := func(ws api.WorkspaceID) tasks.Provider {
		b := tasks.NewBeads(ws, t.TempDir())
		b.SetExec(exe, []string{EnvFakeBd + "=1"})
		return b
	}
	sock := fakeDaemon(t, daemon.WithTaskFactory(factory))
	c := dial(t, sock)

	var created api.Task
	mustCall(t, c, "task.create", api.TaskCreateParams{WorkspaceID: "ws1", Title: "via bd"}, &created)
	if created.Ref.ID != "fake-1" || created.Title != "via bd" {
		t.Fatalf("created via fake bd = %+v", created)
	}
}
