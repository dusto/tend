package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/daemon"
	"github.com/dusto/tend/internal/events"
	"github.com/dusto/tend/internal/rpc"
)

// startTurn registers an editor client, starts a codex session against the fake
// ACP, subscribes its stream, and runs one prompt, returning the client and the
// session's stream id once the turn's events have arrived.
func startTurn(t *testing.T, sock string) (*Client, api.StreamID) {
	t.Helper()
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"},
		WorktreeRoot: t.TempDir(),
	}, &started)
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})
	mustCall(t, c, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "hi"}, &api.AgentPromptResult{})

	if !c.WaitEventCount(started.StreamID, 5, 3*time.Second) {
		t.Fatalf("turn events did not arrive; got %v", c.EventTypes(started.StreamID))
	}
	return c, started.StreamID
}

// TestReplayReconnectDedup proves at-least-once replay + dedup over the socket: a
// reconnect from an older cursor replays the tail, and the client's Deduper drops
// the redelivered records by (stream_id, seq, kind).
func TestReplayReconnectDedup(t *testing.T) {
	sock := fakeDaemon(t)
	first, stream := startTurn(t, sock)

	// The first connection processed seqs 1..5.
	dd := events.NewDeduper()
	got := first.Events(stream)
	if len(got) != 5 {
		t.Fatalf("first delivery = %d events, want 5", len(got))
	}
	for _, ev := range got {
		if !dd.Fresh(ev) {
			t.Errorf("first delivery seq %d should be fresh", ev.Seq)
		}
	}

	// Reconnect with a cursor behind what was processed (last persisted = 2): the
	// daemon replays (2, tail] = 3,4,5, which the Deduper drops as duplicates.
	reconnect := dial(t, sock)
	mustCall(t, reconnect, "events.subscribe", api.EventsSubscribeParams{StreamID: stream, LastSeq: 2}, &api.EventsSubscribeResult{})
	if !reconnect.WaitEventCount(stream, 3, 3*time.Second) {
		t.Fatalf("replay did not redeliver the tail; got %v", reconnect.EventTypes(stream))
	}
	replayed := reconnect.Events(stream)
	for _, ev := range replayed {
		if ev.Seq < 3 {
			t.Errorf("replay redelivered seq %d, want only (2, tail]", ev.Seq)
		}
		if dd.Fresh(ev) {
			t.Errorf("redelivered seq %d should dedup as already-seen", ev.Seq)
		}
	}
}

// startSession starts a codex session for a task and returns its start result;
// the caller has already registered the client.
func startSession(t *testing.T, c *Client, taskID string) api.AgentStartResult {
	t.Helper()
	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: taskID},
		WorktreeRoot: t.TempDir(),
	}, &started)
	return started
}

// TestMultipleSessionsIndependent runs two sessions through one daemon and
// connection and asserts they are fully independent: distinct stream ids, each
// turn delivered only on its own stream (no leakage), and on reconnect both
// subscriptions are restored from their own cursors with per-stream replay and
// dedup.
func TestMultipleSessionsIndependent(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	a := startSession(t, c, "t1")
	b := startSession(t, c, "t2")
	if a.StreamID == b.StreamID || a.SessionID == b.SessionID {
		t.Fatalf("sessions collided: a=%+v b=%+v", a, b)
	}

	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: a.StreamID}, &api.EventsSubscribeResult{})
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: b.StreamID}, &api.EventsSubscribeResult{})

	// Run a turn on each session.
	mustCall(t, c, "agent.prompt", api.AgentPromptParams{SessionID: a.SessionID, Text: "hi a"}, &api.AgentPromptResult{})
	mustCall(t, c, "agent.prompt", api.AgentPromptParams{SessionID: b.SessionID, Text: "hi b"}, &api.AgentPromptResult{})

	if !c.WaitEventCount(a.StreamID, 5, 3*time.Second) || !c.WaitEventCount(b.StreamID, 5, 3*time.Second) {
		t.Fatalf("turns incomplete: a=%v b=%v", c.EventTypes(a.StreamID), c.EventTypes(b.StreamID))
	}
	// No leakage: each stream carries exactly its own turn (5 records), and every
	// record's stream id matches.
	for _, s := range []api.StreamID{a.StreamID, b.StreamID} {
		evs := c.Events(s)
		if len(evs) != 5 {
			t.Errorf("stream %s = %d events, want 5 (leakage or loss)", s, len(evs))
		}
		for _, ev := range evs {
			if ev.StreamID != s {
				t.Errorf("event with stream %s delivered on %s", ev.StreamID, s)
			}
		}
	}

	// Seed a Deduper with the first connection's records for both streams.
	dd := events.NewDeduper()
	for _, s := range []api.StreamID{a.StreamID, b.StreamID} {
		for _, ev := range c.Events(s) {
			if !dd.Fresh(ev) {
				t.Errorf("first-delivery %s seq %d should be fresh", s, ev.Seq)
			}
		}
	}

	// Reconnect: one client restores both subscriptions from independent cursors —
	// stream a behind at 2, stream b fresh at 0.
	rc := dial(t, sock)
	mustCall(t, rc, "events.subscribe", api.EventsSubscribeParams{StreamID: a.StreamID, LastSeq: 2}, &api.EventsSubscribeResult{})
	mustCall(t, rc, "events.subscribe", api.EventsSubscribeParams{StreamID: b.StreamID, LastSeq: 0}, &api.EventsSubscribeResult{})
	if !rc.WaitEventCount(a.StreamID, 3, 3*time.Second) || !rc.WaitEventCount(b.StreamID, 5, 3*time.Second) {
		t.Fatalf("reconnect replay incomplete: a=%v b=%v", rc.EventTypes(a.StreamID), rc.EventTypes(b.StreamID))
	}
	// Independent cursors: a replays only (2, tail] = 3,4,5; b replays the whole 1..5.
	for _, ev := range rc.Events(a.StreamID) {
		if ev.Seq < 3 {
			t.Errorf("stream a replayed seq %d, want only (2, tail]", ev.Seq)
		}
	}
	if len(rc.Events(b.StreamID)) != 5 {
		t.Errorf("stream b replay = %d, want 5", len(rc.Events(b.StreamID)))
	}
	// Per-stream dedup: every replayed record (same seqs as before, even across
	// the two streams) is recognized as already-seen.
	for _, s := range []api.StreamID{a.StreamID, b.StreamID} {
		for _, ev := range rc.Events(s) {
			if dd.Fresh(ev) {
				t.Errorf("replayed %s seq %d should dedup as already-seen", s, ev.Seq)
			}
		}
	}
}

// TestConcurrentSessionStarts starts many sessions concurrently on one
// {workspace, provider} — which the pool may serve by spawning several provider
// processes — and asserts every session gets a distinct id and stream with no
// errors or collisions.
func TestConcurrentSessionStarts(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	const n = 6
	worktree := t.TempDir()
	results := make([]api.AgentStartResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs[i] = c.Call(ctx, "agent.start", api.AgentStartParams{
				ProviderID:   "codex",
				Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: fmt.Sprintf("t%d", i)},
				WorktreeRoot: worktree,
			}, &results[i])
		}(i)
	}
	wg.Wait()

	sessions := make(map[api.SessionID]bool, n)
	streams := make(map[api.StreamID]bool, n)
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("concurrent start %d: %v", i, errs[i])
		}
		r := results[i]
		if r.SessionID == "" {
			t.Fatalf("start %d: empty session id", i)
		}
		if sessions[r.SessionID] {
			t.Errorf("duplicate session id %q across concurrent starts", r.SessionID)
		}
		if streams[r.StreamID] {
			t.Errorf("duplicate stream id %q across concurrent starts", r.StreamID)
		}
		sessions[r.SessionID] = true
		streams[r.StreamID] = true
	}
}

// TestPerStreamMultiplexing proves logical streams are independent over one
// connection: the session turn and a workspace task event land on their own
// streams with no cross-contamination.
func TestPerStreamMultiplexing(t *testing.T) {
	sock := fakeDaemon(t)
	c, sessionStream := startTurn(t, sock)
	workspaceStream := api.WorkspaceStream("ws1")

	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: workspaceStream}, &api.EventsSubscribeResult{})
	mustCall(t, c, "task.create", api.TaskCreateParams{WorkspaceID: "ws1", Title: "do it"}, &api.Task{})

	// The workspace stream carries the provider_started from the turn's lazy
	// spawn (seq 1) and then the task_created (seq 2): both are repo-wide events.
	if !c.WaitEventCount(workspaceStream, 2, 3*time.Second) {
		t.Fatalf("workspace stream missing events; got %v", c.EventTypes(workspaceStream))
	}
	if ws := c.EventTypes(workspaceStream); len(ws) != 2 || ws[0] != "provider_started" || ws[1] != "task_created" {
		t.Errorf("workspace stream = %v, want [provider_started task_created]", ws)
	}
	// The session stream still carries only the turn, not the task event.
	for _, typ := range c.EventTypes(sessionStream) {
		if typ == "task_created" {
			t.Error("task event leaked onto the session stream")
		}
	}
}

// TestCursorCompactedAndSummaryAdvance proves compaction replay over the socket:
// a cursor before a compacted range receives the summary (advancing across the
// range) then the tail; a cursor inside the range is rejected with
// cursor_compacted.
func TestCursorCompactedAndSummaryAdvance(t *testing.T) {
	// Retention 0 so a recent range can be compacted in the test.
	srv, sock, err := StartServer(t.TempDir(), daemon.WithEventRetention(0))
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	store := srv.EventStore()
	stream := api.StreamID("session:comp")
	for range 5 {
		if _, err := store.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession, Type: "tool_call"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	if err := store.Compact(stream, api.ScopeSession, 2, 4, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// A cursor before the range: raw 1, the summary (served at 2, advancing the
	// cursor to 4), then raw 5.
	c := dial(t, sock)
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: stream, LastSeq: 0}, &api.EventsSubscribeResult{})
	if !c.WaitEventCount(stream, 3, 3*time.Second) {
		t.Fatalf("compacted replay incomplete; got %v", c.EventTypes(stream))
	}
	got := c.Events(stream)
	if got[0].Seq != 1 || got[0].Kind != api.KindEvent {
		t.Errorf("record 0 = %+v, want raw seq 1", got[0])
	}
	if got[1].Kind != api.KindSummary || got[1].Seq != 2 || got[1].CursorSeq != 4 {
		t.Errorf("record 1 = %+v, want summary seq 2 cursor 4", got[1])
	}
	if got[2].Seq != 5 || got[2].Kind != api.KindEvent {
		t.Errorf("record 2 = %+v, want raw seq 5", got[2])
	}

	// A cursor inside [2,4): cursor_compacted at the from boundary.
	inside := dial(t, sock)
	cx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = inside.Call(cx, "events.subscribe", api.EventsSubscribeParams{StreamID: stream, LastSeq: 3}, &api.EventsSubscribeResult{})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != api.ErrCursorCompacted {
		t.Fatalf("err = %v, want cursor_compacted", err)
	}
}

// TestProviderLifecycle drives provider.list/start/stop over the socket: list
// shows the configured provider idle, start warms it (emitting provider_started
// on the workspace stream), and stop terminates it (emitting provider_stopped).
func TestProviderLifecycle(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})

	workspaceStream := api.WorkspaceStream("ws1")
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: workspaceStream}, &api.EventsSubscribeResult{})

	var list api.ProviderListResult
	mustCall(t, c, "provider.list", api.ProviderListParams{WorkspaceID: "ws1"}, &list)
	if len(list.Providers) != 1 || list.Providers[0].ProviderID != "codex" || list.Providers[0].Running != 0 {
		t.Fatalf("initial list = %+v, want one idle codex", list.Providers)
	}

	var start api.ProviderStartResult
	mustCall(t, c, "provider.start", api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "codex", WorktreeRoot: t.TempDir()}, &start)
	if start.Running != 1 {
		t.Fatalf("start running = %d, want 1", start.Running)
	}
	if !c.WaitEventCount(workspaceStream, 1, 3*time.Second) {
		t.Fatalf("no provider_started; got %v", c.EventTypes(workspaceStream))
	}
	if ws := c.EventTypes(workspaceStream); ws[0] != "provider_started" {
		t.Errorf("first workspace event = %q, want provider_started", ws[0])
	}

	mustCall(t, c, "provider.list", api.ProviderListParams{WorkspaceID: "ws1"}, &list)
	if list.Providers[0].Running != 1 {
		t.Errorf("list after start: running = %d, want 1", list.Providers[0].Running)
	}

	var stop api.ProviderStopResult
	mustCall(t, c, "provider.stop", api.ProviderStopParams{WorkspaceID: "ws1", ProviderID: "codex"}, &stop)
	if stop.Stopped != 1 {
		t.Fatalf("stop stopped = %d, want 1", stop.Stopped)
	}
	if !c.WaitEventCount(workspaceStream, 2, 3*time.Second) {
		t.Fatalf("no provider_stopped; got %v", c.EventTypes(workspaceStream))
	}
	if ws := c.EventTypes(workspaceStream); ws[len(ws)-1] != "provider_stopped" {
		t.Errorf("last workspace event = %q, want provider_stopped", ws[len(ws)-1])
	}
}

// TestSlashCommandsAggregated drives the daemon's slash-command aggregation over
// the socket: slash.list returns the daemon commands before the agent advertises
// any, and once the agent sends available_commands_update (via the "commands"
// prompt), a slash_commands_updated event arrives on the session stream and
// slash.list reflects the merged provider + daemon set.
func TestSlashCommandsAggregated(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t1"},
		WorktreeRoot: t.TempDir(),
	}, &started)
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})

	// Before the agent advertises anything, only the daemon commands are offered.
	var list api.SlashListResult
	mustCall(t, c, "slash.list", api.SlashListParams{SessionID: started.SessionID}, &list)
	if len(list.Commands) == 0 {
		t.Fatal("slash.list returned no commands; want the daemon set")
	}
	for _, cmd := range list.Commands {
		if cmd.Origin != api.SlashOriginDaemon {
			t.Fatalf("pre-advertise command %q origin = %q, want daemon", cmd.Name, cmd.Origin)
		}
	}
	daemonCount := len(list.Commands)

	// The "commands" prompt makes the fake agent advertise two provider commands.
	mustCall(t, c, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "commands"}, &api.AgentPromptResult{})

	if !waitForEventType(c, started.StreamID, "slash_commands_updated", 3*time.Second) {
		t.Fatalf("no slash_commands_updated; got %v", c.EventTypes(started.StreamID))
	}

	mustCall(t, c, "slash.list", api.SlashListParams{SessionID: started.SessionID}, &list)
	if len(list.Commands) != daemonCount+2 {
		t.Fatalf("merged set = %d commands, want %d daemon + 2 provider", len(list.Commands), daemonCount)
	}
	var review *api.SlashCommand
	for i := range list.Commands {
		if list.Commands[i].Name == "review" {
			review = &list.Commands[i]
		}
	}
	if review == nil || review.Origin != api.SlashOriginProvider || review.ArgHint != "<path>" {
		t.Errorf("review command = %+v, want provider origin with hint <path>", review)
	}
}

// waitForEventType reports whether an event of type typ arrives on stream within d.
func waitForEventType(c *Client, stream api.StreamID, typ string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, got := range c.EventTypes(stream) {
			if got == typ {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestSlashCompleteOverSocket drives slash.complete end-to-end: it completes a
// task command's argument against tasks created through task.create, completes a
// status for /tasks, and returns nothing for a non-daemon command.
func TestSlashCompleteOverSocket(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t-seed"},
		WorktreeRoot: t.TempDir(),
	}, &started)

	mustCall(t, c, "task.create", api.TaskCreateParams{WorkspaceID: "ws1", Title: "first"}, &api.Task{})
	mustCall(t, c, "task.create", api.TaskCreateParams{WorkspaceID: "ws1", Title: "second"}, &api.Task{})

	// A task command completes its id argument against the workspace's tasks.
	var res api.SlashCompleteResult
	mustCall(t, c, "slash.complete", api.SlashCompleteParams{SessionID: started.SessionID, Command: "claim", Prefix: "t"}, &res)
	if len(res.Candidates) < 2 {
		t.Fatalf("claim completion = %v, want at least the two created task ids", res.Candidates)
	}
	if res.Candidates[0].Detail == "" {
		t.Errorf("candidate detail empty; want the task title")
	}

	// /tasks completes a status.
	mustCall(t, c, "slash.complete", api.SlashCompleteParams{SessionID: started.SessionID, Command: "tasks", Prefix: "op"}, &res)
	if len(res.Candidates) != 1 || res.Candidates[0].Value != "open" {
		t.Errorf("tasks completion = %v, want [open]", res.Candidates)
	}

	// A non-daemon command yields nothing — and on the wire as an empty array
	// (schema-required), not null. Capture the raw result to assert the shape.
	var raw json.RawMessage
	mustCall(t, c, "slash.complete", api.SlashCompleteParams{SessionID: started.SessionID, Command: "review", Prefix: "t"}, &raw)
	if string(raw) != `{"candidates":[]}` {
		t.Errorf("provider command completion wire shape = %s, want {\"candidates\":[]}", raw)
	}
}

// TestSlashInvokeOverSocket drives slash.invoke end-to-end: a daemon command runs
// a task action (claiming a task created via task.create), and a non-daemon
// command is forwarded to the agent as a prompt turn.
func TestSlashInvokeOverSocket(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		Task:         api.TaskRef{Provider: "beads", WorkspaceID: "ws1", ID: "t-seed"},
		WorktreeRoot: t.TempDir(),
	}, &started)
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})

	var created api.Task
	mustCall(t, c, "task.create", api.TaskCreateParams{WorkspaceID: "ws1", Title: "do it"}, &created)

	// A daemon command runs the task action and returns the updated task.
	var claim api.SlashInvokeResult
	mustCall(t, c, "slash.invoke", api.SlashInvokeParams{SessionID: started.SessionID, Command: "claim", Args: created.Ref.ID}, &claim)
	if claim.Origin != api.SlashOriginDaemon || claim.Task == nil || claim.Task.Status != "in_progress" {
		t.Fatalf("claim result = %+v, want daemon origin with the task now in_progress", claim)
	}

	// A non-daemon command is forwarded to the agent: the turn runs and streams
	// its events onto the session stream.
	var fwd api.SlashInvokeResult
	mustCall(t, c, "slash.invoke", api.SlashInvokeParams{SessionID: started.SessionID, Command: "review", Args: "src/foo.go"}, &fwd)
	if fwd.Origin != api.SlashOriginProvider || fwd.StopReason == "" {
		t.Errorf("forward result = %+v, want provider origin with a stop reason", fwd)
	}
	if !waitForEventType(c, started.StreamID, "agent_message_chunk", 3*time.Second) {
		t.Errorf("forwarded command produced no turn events; got %v", c.EventTypes(started.StreamID))
	}
}
