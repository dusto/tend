package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// The MCP editor-tools bridge (see docs/adr/0004) is a `tend mcp` process an ACP
// agent spawns from a session's mcpServers declaration. It lets an agent that
// ignores ACP's client fs (e.g. Kiro) still read and — supervised — write the
// editor's buffers by calling tend's tools. The daemon declares the bridge here,
// scoped to the session by a token the bridge resolves with mcp.resolve, since
// the provider only assigns the session id after session/new returns.

// mcpServerDecl is one entry of session/new.mcpServers: a stdio MCP server the
// agent launches as `command args...`. Field names match the ACP wire shape.
type mcpServerDecl struct {
	Name    string      `json:"name"`
	Command string      `json:"command"`
	Args    []string    `json:"args"`
	Env     []mcpEnvVar `json:"env"`
}

// mcpEnvVar is one ACP EnvVariable for an mcpServerDecl.
type mcpEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// newBridgeToken returns a fresh, unguessable token correlating a spawned bridge
// to its session.
func newBridgeToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// bridgeCommand returns the tend CLI to launch the MCP bridge: the tend binary
// shipped alongside the running tendd (GoReleaser ships both together), falling
// back to a bare "tend" resolved on the agent's PATH when there is no sibling
// (dev builds, `go install`).
func bridgeCommand() string {
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "tend")
		if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
			return sibling
		}
	}
	return "tend"
}

// mcpBridgeDeclaration builds the mcpServers entry that spawns the bridge for a
// session: `tend mcp --socket <socket> --bridge <token>`. The bridge dials the
// same daemon on socket and resolves token to the session it serves.
func mcpBridgeDeclaration(socketPath, token string) (json.RawMessage, error) {
	decl := mcpServerDecl{
		Name:    "tend",
		Command: bridgeCommand(),
		Args:    []string{"mcp", "--socket", socketPath, "--bridge", token},
		Env:     []mcpEnvVar{},
	}
	return json.Marshal(decl)
}

// registerBridge records that the bridge spawned with token serves session id,
// so mcp.resolve can answer the bridge when it dials back.
func (s *Service) registerBridge(token string, id api.SessionID) {
	s.bridgeMu.Lock()
	s.bridgeTokens[token] = id
	s.bridgeMu.Unlock()
}

// unbindBridge drops any bridge token pointing at id, called when the session
// ends so a stale token cannot resolve.
func (s *Service) unbindBridge(id api.SessionID) {
	s.bridgeMu.Lock()
	for token, sid := range s.bridgeTokens {
		if sid == id {
			delete(s.bridgeTokens, token)
		}
	}
	s.bridgeMu.Unlock()
}

// ResolveBridge answers mcp.resolve: it maps a bridge token to the session the
// bridge serves. It rejects an empty or unknown token, and a token whose session
// has already ended, so a bridge never silently binds to the wrong session.
func (s *Service) ResolveBridge(_ context.Context, p api.MCPResolveParams) (api.MCPResolveResult, error) {
	if p.Token == "" {
		return api.MCPResolveResult{}, invalidParams("token is required")
	}
	s.bridgeMu.Lock()
	id, ok := s.bridgeTokens[p.Token]
	s.bridgeMu.Unlock()
	if !ok {
		return api.MCPResolveResult{}, invalidParams("unknown bridge token")
	}
	if _, live := s.sessions.Get(id); !live {
		return api.MCPResolveResult{}, invalidParams("session for bridge token has ended")
	}
	return api.MCPResolveResult{SessionID: id}, nil
}

// daemonSocketPath is the socket the bridge dials back on. It is the daemon's
// listen path (rpc.SocketPath), overridable in tests.
var daemonSocketPath = rpc.SocketPath
