package main

import (
	"context"
	"fmt"
	"net"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/handshake"
	"github.com/dusto/tend/internal/rpc"
)

// minPluginToDaemon is the lowest plugin_to_daemon contract the CLI needs:
// session.list landed at 0.8.0, and the resource/stat fields degrade cleanly when
// a daemon predates them, so the CLI does not require the newer version.
const minPluginToDaemon = "0.8.0"

// listSessions connects to the daemon at socket, performs the client handshake
// as an observer, and returns every session (optionally filtered to workspace).
// It reuses the existing daemon_to_client contract — no CLI-specific wire surface.
func listSessions(ctx context.Context, socket, workspace string) ([]api.SessionInfo, error) {
	nc, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to tendd at %s: %w (is the daemon running?)", socket, err)
	}
	conn := rpc.NewConn(nc, nil)
	defer func() { _ = conn.Close() }()

	// Handshake first: verify the daemon meets the minimum contract before issuing
	// method calls, so a version mismatch fails clearly rather than at a later call.
	if _, err := handshake.Do(ctx, conn, api.Versions{PluginToDaemon: minPluginToDaemon}); err != nil {
		return nil, fmt.Errorf("daemon handshake: %w", err)
	}
	// Register as an observer: a read-only client that lists and watches but does
	// not serve editor operations or answer prompts.
	if err := conn.Call(ctx, "client.register", api.ClientRegisterParams{
		ClientID: "tend-cli",
		Role:     api.RoleObserver,
	}, &api.ClientRegisterResult{}); err != nil {
		return nil, fmt.Errorf("client.register: %w", err)
	}

	var res api.SessionListResult
	if err := conn.Call(ctx, "session.list", api.SessionListParams{
		WorkspaceID: api.WorkspaceID(workspace),
	}, &res); err != nil {
		return nil, fmt.Errorf("session.list: %w", err)
	}
	return res.Sessions, nil
}
