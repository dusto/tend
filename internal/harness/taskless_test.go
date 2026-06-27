package harness

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// TestTasklessSessionConversesButCannotMutate drives the task-optional path over
// a real daemon: a session started with a workspace and no task can run a
// prompt turn (conversation), is listed with no task, but is refused a file
// mutation — work stays task-gated.
func TestTasklessSessionConversesButCannotMutate(t *testing.T) {
	sock := fakeDaemon(t)
	c := dial(t, sock)
	mustCall(t, c, "daemon.hello", api.HelloParams{}, &api.HelloResult{})
	mustCall(t, c, "client.register", api.ClientRegisterParams{ClientID: "ed", Role: api.RoleEditor, PromptCapable: true}, &api.ClientRegisterResult{})

	// Start a session with no task — just a provider + workspace + worktree.
	root := t.TempDir()
	var started api.AgentStartResult
	mustCall(t, c, "agent.start", api.AgentStartParams{
		ProviderID:   "codex",
		WorkspaceID:  "ws1",
		WorktreeRoot: root,
	}, &started)
	if started.SessionID == "" {
		t.Fatalf("task-less start = %+v", started)
	}

	// It converses: a prompt turn runs and streams events.
	mustCall(t, c, "events.subscribe", api.EventsSubscribeParams{StreamID: started.StreamID}, &api.EventsSubscribeResult{})
	mustCall(t, c, "agent.prompt", api.AgentPromptParams{SessionID: started.SessionID, Text: "hi"}, &api.AgentPromptResult{})
	if !c.WaitEventCount(started.StreamID, 5, 3*time.Second) {
		t.Fatalf("task-less turn did not stream; got %v", c.EventTypes(started.StreamID))
	}

	// It is listed with no task.
	var list api.SessionListResult
	mustCall(t, c, "session.list", api.SessionListParams{}, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].Task != nil {
		t.Fatalf("session.list = %+v, want one task-less session", list.Sessions)
	}

	// But work is refused: a file mutation needs a task.
	uri := "file://" + filepath.Join(root, "a.go")
	cx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Call(cx, "file.write", api.FileWriteParams{
		SessionID: started.SessionID, URI: uri, Content: "x\n",
		Base: api.FileBase{ContentHash: "deadbeef"},
	}, &api.FileMutationResult{})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidParams {
		t.Fatalf("file.write from a task-less session err = %v, want invalid params (task required)", err)
	}
}
