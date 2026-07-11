package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func newServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tend.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv, err := New(ln, filepath.Join(dir, "events.log"))
	if err != nil {
		_ = ln.Close()
		t.Fatalf("New: %v", err)
	}
	return srv, path
}

func dial(t *testing.T, path string) *rpc.Conn {
	t.Helper()
	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := rpc.NewConn(nc, nil)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestServeHandshake(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	res, err := handshake.Do(testCtx(t), client, api.CurrentVersions())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if res.DaemonEpoch != api.DaemonEpoch(srv.Epoch()) {
		t.Fatalf("epoch = %q, want %q", res.DaemonEpoch, srv.Epoch())
	}
	if res.Versions != api.CurrentVersions() {
		t.Fatalf("versions = %+v", res.Versions)
	}
}

func TestShutdownStopsServeAndClosesConns(t *testing.T) {
	srv, path := newServer(t)
	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()

	client := dial(t, path)
	// Complete a handshake so the server has accepted and is tracking the conn.
	if _, err := handshake.Do(testCtx(t), client, api.Versions{}); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	srv.Shutdown()

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}

	select {
	case <-client.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("client conn not closed by Shutdown")
	}
}

// TestShutdownWaitsForConnGoroutines pins the graceful-shutdown contract: once
// Shutdown returns, every per-conn serving goroutine has finished, so no conn
// remains tracked. It guards the invariant, not the specific accept/register
// ordering hazard, which has no observable symptom from outside the package.
func TestShutdownWaitsForConnGoroutines(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()

	const n = 8
	for i := range n {
		client := dial(t, path)
		// Handshake so the server has accepted and is tracking the conn.
		if _, err := handshake.Do(testCtx(t), client, api.Versions{}); err != nil {
			t.Fatalf("handshake %d: %v", i, err)
		}
	}

	srv.Shutdown()

	if got := srv.connCount(); got != 0 {
		t.Fatalf("after Shutdown: %d conns still tracked, want 0", got)
	}
}

func TestWorkspaceOpenCurrent(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	repo := initRepo(t)
	client := dial(t, path)

	var opened api.WorkspaceInfo
	if err := client.Call(testCtx(t), "workspace.open", api.WorkspaceOpenParams{Dir: repo}, &opened); err != nil {
		t.Fatalf("workspace.open: %v", err)
	}
	if opened.WorkspaceID == "" || opened.Ephemeral {
		t.Fatalf("open returned %+v", opened)
	}
	if opened.DaemonEpoch != api.DaemonEpoch(srv.Epoch()) {
		t.Errorf("DaemonEpoch = %q, want %q", opened.DaemonEpoch, srv.Epoch())
	}

	var current api.WorkspaceInfo
	if err := client.Call(testCtx(t), "workspace.current", api.WorkspaceCurrentParams{}, &current); err != nil {
		t.Fatalf("workspace.current: %v", err)
	}
	if current != opened {
		t.Errorf("current = %+v, want %+v", current, opened)
	}
}

func TestWorkspaceCurrentBeforeOpen(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	err := client.Call(testCtx(t), "workspace.current", api.WorkspaceCurrentParams{}, nil)
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != api.ErrNoActiveWorkspace {
		t.Fatalf("current before open = %v, want rpc.Error code %d", err, api.ErrNoActiveWorkspace)
	}
}

// TestWorkspaceCurrentPerConnection verifies each connection has its own active
// workspace: opening on one connection does not change another's current.
func TestWorkspaceCurrentPerConnection(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	repoA, repoB := initRepo(t), initRepo(t)
	connA, connB := dial(t, path), dial(t, path)

	var a, b api.WorkspaceInfo
	if err := connA.Call(testCtx(t), "workspace.open", api.WorkspaceOpenParams{Dir: repoA}, &a); err != nil {
		t.Fatalf("open A: %v", err)
	}
	if err := connB.Call(testCtx(t), "workspace.open", api.WorkspaceOpenParams{Dir: repoB}, &b); err != nil {
		t.Fatalf("open B: %v", err)
	}
	if a.WorkspaceID == b.WorkspaceID {
		t.Fatal("distinct repos resolved to the same workspace")
	}

	var curA api.WorkspaceInfo
	if err := connA.Call(testCtx(t), "workspace.current", api.WorkspaceCurrentParams{}, &curA); err != nil {
		t.Fatalf("current A: %v", err)
	}
	if curA != a {
		t.Errorf("conn A current = %+v, want its own %+v (not B's)", curA, a)
	}
}

// TestEventsSubscribeWired confirms the event subscription methods are routed by
// the live daemon: a client can subscribe over the socket and gets a Tail back
// (0 for an empty stream).
func TestEventsSubscribeWired(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	var res api.EventsSubscribeResult
	if err := client.Call(testCtx(t), "events.subscribe", api.EventsSubscribeParams{StreamID: "session:x"}, &res); err != nil {
		t.Fatalf("events.subscribe: %v", err)
	}
	if res.Tail != 0 {
		t.Errorf("Tail = %d, want 0 for empty stream", res.Tail)
	}
}

func TestFileReadWired(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	// No such session: proves file.read is registered and routed (it reaches the
	// service, which rejects the unknown session) rather than method-not-found.
	err := client.Call(testCtx(t), "file.read",
		api.FileReadParams{SessionID: "nope", URI: "file:///repo/a.go"}, nil)
	if err == nil {
		t.Fatal("file.read with an unknown session should error")
	}
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid-params rpc error", err)
	}
}

func TestFilePatchWired(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	// Unknown session: reaches the file service (rejected) rather than
	// method-not-found, proving file.patch is registered and routed.
	err := client.Call(testCtx(t), "file.patch",
		api.FilePatchParams{SessionID: "nope", URI: "file:///repo/a.go"}, nil)
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid-params rpc error", err)
	}
}

func TestApprovalRespondCapabilityGated(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	respond := func(c *rpc.Conn) error {
		return c.Call(testCtx(t), "approval.respond",
			api.ApprovalRespondParams{ApprovalID: "ghost", Approved: true}, nil)
	}

	// An observer (not prompt-capable) is refused before the approval is even
	// looked up.
	observer := dial(t, path)
	if _, err := register(t, observer, "obs", api.RoleObserver, false); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	err := respond(observer)
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != api.ErrNotPromptCapable {
		t.Fatalf("observer respond err = %v, want not_prompt_capable", err)
	}

	// A prompt-capable client passes the gate and is told the approval is unknown.
	editor := dial(t, path)
	if _, err := register(t, editor, "ed", api.RoleEditor, true); err != nil {
		t.Fatalf("register editor: %v", err)
	}
	err = respond(editor)
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidParams {
		t.Fatalf("editor respond err = %v, want invalid-params (unknown approval)", err)
	}
}

func register(t *testing.T, c *rpc.Conn, id string, role api.ClientRole, promptCapable bool) (api.ClientRegisterResult, error) {
	t.Helper()
	var res api.ClientRegisterResult
	err := c.Call(testCtx(t), "client.register",
		api.ClientRegisterParams{ClientID: api.ClientID(id), Role: role, PromptCapable: promptCapable}, &res)
	return res, err
}

func TestTaskMethodsWired(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	var created api.Task
	if err := client.Call(testCtx(t), "task.create",
		api.TaskCreateParams{WorkspaceID: "ws1", Title: "do it"}, &created); err != nil {
		t.Fatalf("task.create: %v", err)
	}
	if created.Ref.ID == "" || created.Title != "do it" {
		t.Fatalf("created = %+v", created)
	}

	var list api.TaskListResult
	if err := client.Call(testCtx(t), "task.list", api.TaskListParams{WorkspaceID: "ws1"}, &list); err != nil {
		t.Fatalf("task.list: %v", err)
	}
	if len(list.Tasks) != 1 {
		t.Fatalf("list = %+v", list)
	}
}

func TestPaneMethodsWired(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	// User-initiated open (no session) is ungated.
	var opened api.PaneInfo
	if err := client.Call(testCtx(t), "pane.open", api.PaneOpenParams{WorkspaceID: "ws1", Cwd: t.TempDir()}, &opened); err != nil {
		t.Fatalf("pane.open: %v", err)
	}
	if opened.PaneID == "" || !opened.Running {
		t.Fatalf("opened = %+v", opened)
	}

	var list api.PaneListResult
	if err := client.Call(testCtx(t), "pane.list", api.PaneListParams{WorkspaceID: "ws1"}, &list); err != nil {
		t.Fatalf("pane.list: %v", err)
	}
	if len(list.Panes) != 1 || list.Panes[0].PaneID != opened.PaneID {
		t.Fatalf("list = %+v", list.Panes)
	}

	if err := client.Call(testCtx(t), "pane.close", api.PaneCloseParams{PaneID: opened.PaneID}, &api.PaneCloseResult{}); err != nil {
		t.Fatalf("pane.close: %v", err)
	}

	// pane.run is registered and routes to the service (unknown pane is rejected
	// there rather than method-not-found).
	err := client.Call(testCtx(t), "pane.run", api.PaneRunParams{PaneID: "nope", Command: "ls", SessionID: "s1"}, &api.PaneRunResult{})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidParams {
		t.Fatalf("pane.run err = %v, want invalid-params rpc error", err)
	}
}

func TestApplyChangeSetWired(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	// Unknown session: routes to the file service (rejected there) rather than
	// method-not-found, proving file.apply_change_set is registered.
	err := client.Call(testCtx(t), "file.apply_change_set",
		api.FileApplyChangeSetParams{SessionID: "nope"}, nil)
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid-params rpc error", err)
	}
}

func TestShutdownIdempotent(t *testing.T) {
	srv, _ := newServer(t)
	go func() { _ = srv.Serve() }()
	srv.Shutdown()
	srv.Shutdown() // must not panic or block
}

func TestClientRegisterAndDisconnect(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	client := dial(t, path)
	var res api.ClientRegisterResult
	if err := client.Call(testCtx(t), "client.register",
		api.ClientRegisterParams{ClientID: "c1", Role: api.RoleEditor, PromptCapable: true}, &res); err != nil {
		t.Fatalf("client.register: %v", err)
	}
	if res.ClientID != "c1" {
		t.Fatalf("registered id = %q", res.ClientID)
	}
	cl, ok := srv.clients.Get("c1")
	if !ok || !cl.IsEditor() || !cl.CanRespondToPrompts() {
		t.Fatalf("registry entry = %+v, ok=%v", cl, ok)
	}

	// Disconnecting removes the client identity from the registry.
	_ = client.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := srv.clients.Get("c1"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("client identity not removed after disconnect")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestMemoryContextEndToEnd drives the new memory.context method over a real
// socket: it writes an always-steering entry, then asks for the assembled,
// budget-bounded context and checks the steering body comes back. This exercises
// dispatch registration, schema validation, and the 0.23.0 handshake version.
func TestMemoryContextEndToEnd(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	repo := initRepo(t)
	client := dial(t, path)
	if _, err := handshake.Do(testCtx(t), client, api.CurrentVersions()); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	var opened api.WorkspaceInfo
	if err := client.Call(testCtx(t), "workspace.open", api.WorkspaceOpenParams{Dir: repo}, &opened); err != nil {
		t.Fatalf("workspace.open: %v", err)
	}

	var written api.MemoryWriteResult
	if err := client.Call(testCtx(t), "memory.write", api.MemoryWriteParams{
		WorkspaceID: opened.WorkspaceID,
		ID:          "house-style",
		Kind:        api.MemoryKindSteering,
		Apply:       api.MemoryApplyAlways,
		Title:       "House style",
		Text:        "Prefer table-driven tests.",
	}, &written); err != nil {
		t.Fatalf("memory.write: %v", err)
	}

	var ctxRes api.MemoryContextResult
	if err := client.Call(testCtx(t), "memory.context", api.MemoryContextParams{
		WorkspaceID: opened.WorkspaceID,
	}, &ctxRes); err != nil {
		t.Fatalf("memory.context: %v", err)
	}
	if ctxRes.Summarized {
		t.Error("small assembly should not be summarized")
	}
	if len(ctxRes.Included) != 1 || ctxRes.Included[0] != "house-style" {
		t.Errorf("included = %v, want [house-style]", ctxRes.Included)
	}
	if !strings.Contains(ctxRes.Text, "Prefer table-driven tests.") {
		t.Errorf("context text missing steering body:\n%s", ctxRes.Text)
	}
}

// TestResumeSeedEndToEnd drives session.resume_seed over a real socket: it seeds
// a prior session's durable event stream directly on the store (standing in for
// a completed session), writes an always-steering memory, then asks the daemon
// to reconstruct a resume seed and checks the seed carries both the prior
// transcript and the workspace memory. This exercises dispatch registration,
// schema validation, the 0.24.0 handshake, and the events+memory+summarize
// composition — all without a live provider, which is the point of daemon-side
// reconstruction.
func TestResumeSeedEndToEnd(t *testing.T) {
	srv, path := newServer(t)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Shutdown)

	repo := initRepo(t)
	client := dial(t, path)
	if _, err := handshake.Do(testCtx(t), client, api.CurrentVersions()); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	var opened api.WorkspaceInfo
	if err := client.Call(testCtx(t), "workspace.open", api.WorkspaceOpenParams{Dir: repo}, &opened); err != nil {
		t.Fatalf("workspace.open: %v", err)
	}

	var written api.MemoryWriteResult
	if err := client.Call(testCtx(t), "memory.write", api.MemoryWriteParams{
		WorkspaceID: opened.WorkspaceID,
		ID:          "house-style",
		Kind:        api.MemoryKindSteering,
		Apply:       api.MemoryApplyAlways,
		Title:       "House style",
		Text:        "Prefer table-driven tests.",
	}, &written); err != nil {
		t.Fatalf("memory.write: %v", err)
	}

	// Seed a prior session's durable transcript directly on the store, as a
	// completed session would have left behind.
	sid := api.SessionID("prior-session")
	stream := api.SessionStream(sid)
	publish := func(typ string, payload any) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s: %v", typ, err)
		}
		if _, err := srv.EventStore().Publish(api.Event{StreamID: stream, Scope: api.ScopeSession, Type: typ, Payload: body}); err != nil {
			t.Fatalf("publish %s: %v", typ, err)
		}
	}
	publish("agent_message_chunk", api.AgentMessageChunk{SessionID: sid, Text: "investigated the parser bug"})
	publish("tool_call", api.ToolCall{SessionID: sid, Name: "grep"})
	publish("turn_end", api.TurnEnd{SessionID: sid})

	var seed api.SessionResumeSeedResult
	if err := client.Call(testCtx(t), "session.resume_seed", api.SessionResumeSeedParams{
		SessionID:   sid,
		WorkspaceID: opened.WorkspaceID,
		Budget:      4000,
	}, &seed); err != nil {
		t.Fatalf("session.resume_seed: %v", err)
	}
	if seed.SourceSessionID != sid {
		t.Errorf("source session = %q, want %q", seed.SourceSessionID, sid)
	}
	if !strings.Contains(seed.Text, "investigated the parser bug") || !strings.Contains(seed.Text, "[tool: grep]") {
		t.Errorf("seed missing prior transcript:\n%s", seed.Text)
	}
	if !strings.Contains(seed.Text, "Prefer table-driven tests.") {
		t.Errorf("seed missing workspace memory:\n%s", seed.Text)
	}
}
