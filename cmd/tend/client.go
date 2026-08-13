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

// dialRegister connects to the daemon at socket, performs the version handshake,
// and registers as an observer — a read-only client that lists and watches but
// does not serve editor operations or answer prompts. The caller owns the
// returned connection and must Close it.
func dialRegister(ctx context.Context, socket, clientID string) (*rpc.Conn, error) {
	nc, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to tendd at %s: %w (is the daemon running?)", socket, err)
	}
	conn := rpc.NewConn(nc, nil)
	// Handshake first: verify the daemon meets the minimum contract before issuing
	// method calls, so a version mismatch fails clearly rather than at a later call.
	if _, err := handshake.Do(ctx, conn, api.Versions{PluginToDaemon: minPluginToDaemon}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon handshake: %w", err)
	}
	if err := conn.Call(ctx, "client.register", api.ClientRegisterParams{
		ClientID: api.ClientID(clientID),
		Role:     api.RoleObserver,
	}, &api.ClientRegisterResult{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("client.register: %w", err)
	}
	return conn, nil
}

// listSessions returns every session (optionally filtered to workspace). It
// reuses the existing daemon_to_client contract — no CLI-specific wire surface.
func listSessions(ctx context.Context, socket, workspace string) ([]api.SessionInfo, error) {
	conn, err := dialRegister(ctx, socket, "tend-cli")
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
	conn, err := dialRegister(ctx, socket, "tend-cli")
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
