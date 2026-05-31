package workspace

import (
	"context"
	"errors"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Method names for the workspace methods, matching the api contract.
const (
	MethodOpen    = "workspace.open"
	MethodCurrent = "workspace.current"
)

// Register installs the workspace methods on m, backed by mgr.
func Register(m *dispatch.Mux, mgr *Manager) error {
	if err := dispatch.Handle(m, MethodOpen,
		func(ctx context.Context, p api.WorkspaceOpenParams) (api.WorkspaceInfo, error) {
			info, err := mgr.Open(ctx, p.Dir)
			if err != nil {
				// Open only fails when p.Dir cannot be resolved.
				return api.WorkspaceInfo{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
			}
			return info, nil
		}); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodCurrent,
		func(_ context.Context, _ api.WorkspaceCurrentParams) (api.WorkspaceInfo, error) {
			info, err := mgr.Current()
			if errors.Is(err, ErrNoActiveWorkspace) {
				return api.WorkspaceInfo{}, &rpc.Error{Code: api.ErrNoActiveWorkspace, Message: err.Error()}
			}
			return info, err
		})
}
