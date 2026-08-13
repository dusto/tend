package api

// MCPResolveParams is the mcp.resolve request. The MCP bridge (`tend mcp`) is
// declared in a session's session/new.mcpServers with a daemon-generated token
// instead of a session id, because the provider assigns the session id in the
// session/new response — after the declaration is already sent. The bridge
// resolves the token to its session id at runtime with mcp.resolve.
type MCPResolveParams struct {
	Token string `json:"token"`
}

// MCPResolveResult is the mcp.resolve reply: the session the bridge serves.
type MCPResolveResult struct {
	SessionID SessionID `json:"session_id"`
}
