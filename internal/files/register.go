package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/patch"
	"github.com/dusto/tend/internal/rpc"
)

// Register installs the file methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	if err := dispatch.Handle(m, MethodRead, func(ctx context.Context, p api.FileReadParams) (api.FileReadResult, error) {
		res, err := s.Read(ctx, p)
		if err != nil {
			return api.FileReadResult{}, toRPCError(err, p.URI)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodOpen, func(ctx context.Context, p api.FileOpenParams) (api.FileOpenResult, error) {
		res, err := s.Open(ctx, p)
		if err != nil {
			return api.FileOpenResult{}, toRPCError(err, p.URI)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodPatch, func(ctx context.Context, p api.FilePatchParams) (api.FileMutationResult, error) {
		res, err := s.Patch(ctx, p)
		if err != nil {
			return api.FileMutationResult{}, toRPCError(err, p.URI)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodWrite, func(ctx context.Context, p api.FileWriteParams) (api.FileMutationResult, error) {
		res, err := s.Write(ctx, p)
		if err != nil {
			return api.FileMutationResult{}, toRPCError(err, p.URI)
		}
		return res, nil
	}); err != nil {
		return err
	}
	if err := dispatch.Handle(m, MethodApplyChangeSet, func(ctx context.Context, p api.FileApplyChangeSetParams) (api.FileApplyChangeSetResult, error) {
		res, err := s.ApplyChangeSet(ctx, p)
		if err != nil {
			// Only structural errors surface as rpc errors; per-target conflicts and
			// failures are reported inside the result.
			return api.FileApplyChangeSetResult{}, toRPCError(err, "")
		}
		return res, nil
	}); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodDiff, func(ctx context.Context, p api.FileDiffParams) (api.FileDiffResult, error) {
		res, err := s.Diff(ctx, p)
		if err != nil {
			if errors.Is(err, ErrUnknownChangeSet) {
				data, _ := json.Marshal(api.UnknownChangeSetData(p))
				return api.FileDiffResult{}, &rpc.Error{Code: api.ErrUnknownChangeSet, Message: err.Error(), Data: data}
			}
			return api.FileDiffResult{}, toRPCError(err, "")
		}
		return res, nil
	})
}

// toRPCError maps a file-service error to the JSON-RPC error sent to the client.
// A stale base becomes a conflict carrying the uri so the agent re-reads and
// re-proposes; bad input becomes invalid-params.
func toRPCError(err error, uri string) error {
	switch {
	case errors.Is(err, patch.ErrConflict):
		data, _ := json.Marshal(api.ConflictData{URI: uri})
		return &rpc.Error{Code: api.ErrConflict, Message: err.Error(), Data: data}
	case errors.Is(err, ErrNoSession), errors.Is(err, ErrNoTask), errors.Is(err, ErrBadURI), errors.Is(err, ErrOutsideWorkspace),
		errors.Is(err, ErrAccessDenied),
		errors.Is(err, patch.ErrInvalidPosition), errors.Is(err, patch.ErrInvalidRange), errors.Is(err, patch.ErrOverlap):
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	default:
		return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}
}

// randomID returns a fresh random hex id for a change set.
func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
