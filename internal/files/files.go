// Package files implements the daemon's repo-file tools. file.read is
// editor-aware: when the file is open in the session's bound editor it returns
// the live buffer content and a changedtick base; otherwise it reads disk and
// returns a content-hash base. The daemon owns the disk read and hash so the
// same code that produces a base also verifies it on a later apply.
package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/editor"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/session"
)

// MethodRead is the file.read method name.
const MethodRead = "file.read"

// Register installs the file methods on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	return dispatch.Handle(m, MethodRead, func(ctx context.Context, p api.FileReadParams) (api.FileReadResult, error) {
		res, err := s.Read(ctx, p)
		if err != nil {
			return api.FileReadResult{}, toRPCError(err)
		}
		return res, nil
	})
}

// toRPCError maps a file-service error to the JSON-RPC error sent to the client.
func toRPCError(err error) error {
	switch {
	case errors.Is(err, ErrNoSession), errors.Is(err, ErrBadURI), errors.Is(err, ErrOutsideWorkspace):
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	default:
		return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
	}
}

// Errors returned by the file service.
var (
	// ErrNoSession reports that no session has the given id.
	ErrNoSession = errors.New("files: unknown session")
	// ErrBadURI reports a uri that is not a usable file:// reference.
	ErrBadURI = errors.New("files: uri is not a file path")
	// ErrOutsideWorkspace reports that a uri resolves outside the session's
	// worktree, so it is refused.
	ErrOutsideWorkspace = errors.New("files: path is outside the session worktree")
)

// editorReader is the slice of editor.Service the file service drives. It is an
// interface so file.read can be tested without a live editor connection.
type editorReader interface {
	ReadBuffer(ctx context.Context, sessionID api.SessionID, p api.EditorReadBufferParams) (api.EditorReadBufferResult, error)
}

// Service implements the file tools over the session registry and the editor
// reverse-RPC. It is safe for concurrent use.
type Service struct {
	sessions *session.Registry
	editors  editorReader
}

// NewService returns a Service backed by the session registry and editor reader.
func NewService(sessions *session.Registry, editors editorReader) *Service {
	return &Service{sessions: sessions, editors: editors}
}

// Read returns the content of a repo file and the base revision it reflects. If
// the file is open in the session's bound editor, it returns the live buffer
// content with a changedtick base; otherwise it reads disk and returns a
// content-hash base.
func (s *Service) Read(ctx context.Context, p api.FileReadParams) (api.FileReadResult, error) {
	sess, ok := s.sessions.Get(p.SessionID)
	if !ok {
		return api.FileReadResult{}, ErrNoSession
	}
	path, err := resolvePath(p.URI, sess.WorktreeRoot)
	if err != nil {
		return api.FileReadResult{}, err
	}

	// Prefer the editor's live buffer when the file is open there.
	rb, err := s.editors.ReadBuffer(ctx, p.SessionID, api.EditorReadBufferParams{URI: p.URI})
	switch {
	case err == nil && rb.Open:
		return api.FileReadResult{Content: rb.Content, Base: rb.Base, Open: true}, nil
	case err != nil && !errors.Is(err, editor.ErrEditorUnavailable):
		// A genuine reverse-call failure (not just a headless session) is surfaced
		// rather than silently masked by a disk read.
		return api.FileReadResult{}, err
	}

	// Headless, or the file is not open: read disk and hash it ourselves.
	data, err := os.ReadFile(path)
	if err != nil {
		return api.FileReadResult{}, fmt.Errorf("files: reading %s: %w", p.URI, err)
	}
	sum := sha256.Sum256(data)
	return api.FileReadResult{
		Content: string(data),
		Base:    api.FileBase{ContentHash: hex.EncodeToString(sum[:])},
		Open:    false,
	}, nil
}

// resolvePath converts a file:// uri to a filesystem path and verifies it falls
// within the worktree root, refusing traversal outside it. Symlinks are resolved
// before the check, so a link inside the worktree that points outside cannot
// escape it.
func resolvePath(uri, worktreeRoot string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", ErrBadURI
	}
	root := resolveSymlinks(filepath.Clean(worktreeRoot))
	path := resolveSymlinks(filepath.Clean(u.Path))
	if !within(root, path) {
		return "", ErrOutsideWorkspace
	}
	return path, nil
}

// resolveSymlinks returns path with symlinks resolved. When the leaf does not
// exist yet (so EvalSymlinks fails), it resolves the longest existing ancestor
// and re-appends the remaining components, so a symlinked parent still cannot
// disguise a path that escapes the worktree.
func resolveSymlinks(path string) string {
	suffix := ""
	for p := path; ; {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path // nothing along the path resolves
		}
		suffix = filepath.Join(filepath.Base(p), suffix)
		p = parent
	}
}

// within reports whether path is root itself or lies beneath it.
func within(root, path string) bool {
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}
