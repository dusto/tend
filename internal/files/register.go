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
	if err := dispatch.Handle(m, MethodPatch, func(ctx context.Context, p api.FilePatchParams) (api.FileMutationResult, error) {
		res, err := s.Patch(ctx, p)
		if err != nil {
			return api.FileMutationResult{}, toRPCError(err, p.URI)
		}
		return res, nil
	}); err != nil {
		return err
	}
	return dispatch.Handle(m, MethodWrite, func(ctx context.Context, p api.FileWriteParams) (api.FileMutationResult, error) {
		res, err := s.Write(ctx, p)
		if err != nil {
			return api.FileMutationResult{}, toRPCError(err, p.URI)
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
	case errors.Is(err, ErrNoSession), errors.Is(err, ErrBadURI), errors.Is(err, ErrOutsideWorkspace),
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
