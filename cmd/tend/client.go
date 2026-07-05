package main

import (
	"context"
	"fmt"
	"net"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

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

	// Handshake, then register as an observer: a read-only client that lists and
	// watches but does not serve editor operations or answer prompts.
	if err := conn.Call(ctx, "daemon.hello", api.HelloParams{}, &api.HelloResult{}); err != nil {
		return nil, fmt.Errorf("daemon.hello: %w", err)
	}
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
