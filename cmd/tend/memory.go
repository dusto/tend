package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/client"
	"github.com/dusto/tend/internal/memimport"
	"github.com/dusto/tend/internal/rpc"
)

// minMemoryProvenance is the lowest plugin_to_daemon contract the importer needs:
// MemoryWriteParams.provenance landed at 0.22.0.
const minMemoryProvenance = "0.22.0"

// daemonStore backs memimport.Store with memory.get / memory.write over the
// daemon socket for one workspace, so imports land in that workspace's configured
// memory dir.
type daemonStore struct {
	conn *client.Conn
	ws   api.WorkspaceID
}

// Get returns the entry for id. memory.get answers a missing id with an
// invalid-params error; since the importer always sends a valid workspace and a
// non-empty id, that error unambiguously means "not found".
func (s *daemonStore) Get(ctx context.Context, id string) (api.MemoryEntry, bool, error) {
	var res api.MemoryGetResult
	err := s.conn.Call(ctx, "memory.get", api.MemoryGetParams{WorkspaceID: s.ws, ID: api.MemoryID(id)}, &res)
	if err != nil {
		var rerr *rpc.Error
		if errors.As(err, &rerr) && rerr.Code == rpc.CodeInvalidParams {
			return api.MemoryEntry{}, false, nil
		}
		return api.MemoryEntry{}, false, err
	}
	return res.Entry, true, nil
}

// Write upserts an entry, binding it to the store's workspace.
func (s *daemonStore) Write(ctx context.Context, p api.MemoryWriteParams) (api.MemoryEntry, error) {
	p.WorkspaceID = s.ws
	var res api.MemoryWriteResult
	if err := s.conn.Call(ctx, "memory.write", p, &res); err != nil {
		return api.MemoryEntry{}, err
	}
	return res.Entry, nil
}

// runImport connects to the daemon, resolves the workspace for dir, and imports
// the selected sources' files into that workspace's memory.
func runImport(ctx context.Context, socket, dir string, sources []string, dryRun bool) (memimport.Result, error) {
	adapters, err := memimport.Select(sources)
	if err != nil {
		return memimport.Result{}, err
	}

	conn, err := client.Dial(ctx, client.Options{
		Socket:            socket,
		ClientID:          "tend-cli",
		Role:              api.RoleObserver,
		MinPluginToDaemon: minMemoryProvenance,
	})
	if err != nil {
		return memimport.Result{}, err
	}
	defer func() { _ = conn.Close() }()

	// Resolve the workspace (and its worktree root) from dir.
	var ws api.WorkspaceInfo
	if err := conn.Call(ctx, "workspace.open", api.WorkspaceOpenParams{Dir: dir}, &ws); err != nil {
		return memimport.Result{}, fmt.Errorf("workspace.open: %w", err)
	}

	store := &daemonStore{conn: conn, ws: ws.WorkspaceID}
	return memimport.Run(ctx, store, ws.WorktreeRoot, adapters, dryRun)
}
