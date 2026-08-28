package main

import (
	"context"
	"fmt"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/client"
)

// minPluginToDaemon is the lowest plugin_to_daemon contract the read-only CLI
// commands need: session.list landed at 0.8.0, and the resource/stat fields
// degrade cleanly when a daemon predates them, so listing does not require a
// newer version. Commands that call newer methods pass a higher minimum to
// dialRegister (e.g. the MCP bridge, whose file tools need much more recent
// methods).
const minPluginToDaemon = "0.8.0"

// dialRegister connects to the daemon at socket as a read-only observer,
// requiring minVersion at the handshake. It is a thin wrapper over the shared
// client package with the CLI's identity/role fixed. The caller owns the
// returned connection and must Close it.
func dialRegister(ctx context.Context, socket, clientID, minVersion string) (*client.Conn, error) {
	return client.Dial(ctx, client.Options{
		Socket:            socket,
		ClientID:          clientID,
		Role:              api.RoleObserver,
		MinPluginToDaemon: minVersion,
	})
}

// listSessions returns every session (optionally filtered to workspace). It
// reuses the existing daemon_to_client contract — no CLI-specific wire surface.
func listSessions(ctx context.Context, socket, workspace string) ([]api.SessionInfo, error) {
	conn, err := dialRegister(ctx, socket, "tend-cli", minPluginToDaemon)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	var res api.SessionListResult
	if err := conn.Call(ctx, "session.list", api.SessionListParams{
		WorkspaceID: api.WorkspaceID(workspace),
	}, &res); err != nil {
		return nil, fmt.Errorf("session.list: %w", err)
	}
	return res.Sessions, nil
}

// stopSession ends a session via agent.stop, releasing its provider process.
// agent.stop is not role-gated, so an observer connection suffices.
func stopSession(ctx context.Context, socket, sessionID string) error {
	conn, err := dialRegister(ctx, socket, "tend-cli", minPluginToDaemon)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Call(ctx, "agent.stop", api.AgentStopParams{
		SessionID: api.SessionID(sessionID),
	}, &struct{}{}); err != nil {
		return fmt.Errorf("agent.stop: %w", err)
	}
	return nil
}
