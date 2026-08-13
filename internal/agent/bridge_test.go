package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/dusto/tend/api"
)

// decodeBridgeDecl unmarshals the single mcpServers entry the manager received
// on session/new and returns it plus the bridge token from its args.
func decodeBridgeDecl(t *testing.T, params []json.RawMessage) (mcpServerDecl, string) {
	t.Helper()
	if len(params) != 1 {
		t.Fatalf("mcpServers = %d entries, want 1", len(params))
	}
	var decl mcpServerDecl
	if err := json.Unmarshal(params[0], &decl); err != nil {
		t.Fatalf("unmarshal mcpServers[0]: %v", err)
	}
	i := slices.Index(decl.Args, "--bridge")
	if i < 0 || i+1 >= len(decl.Args) {
		t.Fatalf("args missing --bridge <token>: %v", decl.Args)
	}
	return decl, decl.Args[i+1]
}

// Start declares the MCP bridge on session/new and binds its token so the
// spawned bridge can resolve which session it serves.
func TestStartDeclaresBridgeAndResolves(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1"}
	svc, _ := newService(t, mgr)

	res, err := svc.Start(context.Background(), api.AgentStartParams{
		ProviderID:   "codex",
		WorkspaceID:  "ws1",
		WorktreeRoot: "/repo/wt",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	decl, token := decodeBridgeDecl(t, mgr.openParams.MCPServers)
	if decl.Name != "tend" {
		t.Errorf("mcp server name = %q, want tend", decl.Name)
	}
	if len(decl.Args) == 0 || decl.Args[0] != "mcp" {
		t.Errorf("args[0] = %v, want mcp", decl.Args)
	}
	if !slices.Contains(decl.Args, "--socket") {
		t.Errorf("args missing --socket: %v", decl.Args)
	}

	// The token resolves to the session the provider assigned.
	got, err := svc.ResolveBridge(context.Background(), api.MCPResolveParams{Token: token})
	if err != nil {
		t.Fatalf("ResolveBridge: %v", err)
	}
	if got.SessionID != res.SessionID {
		t.Errorf("resolved session = %q, want %q", got.SessionID, res.SessionID)
	}
}

// A distinct token is issued per session so bridges never cross-resolve.
func TestStartIssuesDistinctBridgeTokens(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1"}
	svc, _ := newService(t, mgr)
	start := func() string {
		if _, err := svc.Start(context.Background(), api.AgentStartParams{
			ProviderID: "codex", WorkspaceID: "ws1", WorktreeRoot: "/repo/wt",
		}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		_, token := decodeBridgeDecl(t, mgr.openParams.MCPServers)
		return token
	}
	first := start()
	svc.sessions.Remove("sess-1") // free the id so the second Start does not collide
	mgr.openID = "sess-2"
	if second := start(); second == first {
		t.Errorf("tokens not distinct: %q", first)
	}
}

// A token whose session has ended (Stop) no longer resolves.
func TestResolveBridgeAfterStop(t *testing.T) {
	mgr := &fakeManager{openID: "sess-1"}
	svc, _ := newService(t, mgr)
	if _, err := svc.Start(context.Background(), api.AgentStartParams{
		ProviderID: "codex", WorkspaceID: "ws1", WorktreeRoot: "/repo/wt",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, token := decodeBridgeDecl(t, mgr.openParams.MCPServers)

	if _, err := svc.Stop(context.Background(), api.AgentStopParams{SessionID: "sess-1"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := svc.ResolveBridge(context.Background(), api.MCPResolveParams{Token: token}); err == nil {
		t.Error("resolve succeeded after Stop, want error")
	}
}

func TestResolveBridgeRejectsBadToken(t *testing.T) {
	svc, _ := newService(t, &fakeManager{openID: "sess-1"})
	for _, tc := range []struct{ name, token string }{
		{"empty", ""},
		{"unknown", "deadbeef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.ResolveBridge(context.Background(), api.MCPResolveParams{Token: tc.token}); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}
