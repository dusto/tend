package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/urfave/cli/v3"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/client"
	"github.com/dusto/tend/internal/mcp"
	"github.com/dusto/tend/internal/version"
)

// minMCPBridge is the lowest plugin_to_daemon contract the MCP bridge needs: its
// tools call the file methods, and the newest of those, file.open (backing
// open_buffer), landed at 0.30.0. Handshaking against this makes a too-old daemon
// fail clearly at connect rather than advertising open_buffer and then getting a
// missing-method error on the first call. Bump this in step with the newest file
// method any tool calls.
const minMCPBridge = "0.30.0"

// mcpCommand runs tend as a Model Context Protocol server over stdio. An ACP
// agent spawns it (declared via session/new.mcpServers) and calls tend's editor
// tools, so an agent that does not use ACP's client fs callbacks can still work
// through the editor. See docs/adr/0004. The server dials the running daemon and
// routes each tool call to it, scoped to --session.
func mcpCommand() *cli.Command {
	return &cli.Command{
		Name:  "mcp",
		Usage: "run as an MCP server exposing tend's editor tools (spawned by an agent)",
		Description: "Speak the Model Context Protocol over stdio (newline-delimited JSON-RPC 2.0 " +
			"on stdin/stdout) so an ACP agent can call tend's editor tools. Not run by hand; the " +
			"daemon declares it via session/new.mcpServers and the agent spawns it.",
		Flags: []cli.Flag{
			// --session binds this bridge to one session so tool calls route to
			// the right editor. Used when the session id is known up front.
			&cli.StringFlag{Name: "session", Usage: "session id this bridge serves"},
			// --bridge is how the daemon binds a spawned bridge: it declares a
			// token (the session id does not exist yet at declaration time) and
			// the bridge resolves it to a session id via mcp.resolve on first use.
			&cli.StringFlag{Name: "bridge", Usage: "bridge token; resolved to a session id at runtime"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Dial the daemon up front: the bridge is spawned inside a live
			// session, so the daemon is running. Tool calls reuse this one
			// connection (the stdio server processes messages sequentially).
			conn, err := dialRegister(ctx, cmd.String("socket"), "tend-mcp", minMCPBridge)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			srv := mcp.NewServer(mcp.Options{
				Name:    "tend",
				Version: version.Version,
				Tools:   mcp.EditorTools(),
				Caller:  &daemonCaller{conn: conn, session: cmd.String("session"), token: cmd.String("bridge")},
			})
			return srv.Serve(ctx, os.Stdin, os.Stdout)
		},
	}
}

// daemonCaller routes MCP tools/call invocations to the daemon over conn, scoped
// to one session. It implements mcp.ToolCaller.
//
// The session is identified either directly (session, from --session) or by a
// bridge token (token, from --bridge) resolved to a session id at runtime — the
// daemon declares the bridge before the provider assigns the session id, so the
// id is not known until the bridge dials back. Resolution is done once, lazily,
// on the first tool call (always inside a prompt turn, well after the daemon has
// bound the token), and cached.
type daemonCaller struct {
	conn    *client.Conn
	session string
	token   string

	resolveOnce sync.Once
	resolveErr  error
}

// sessionID returns the session this bridge serves, resolving the bridge token
// on first use when no session id was given directly.
func (c *daemonCaller) sessionID(ctx context.Context) (string, error) {
	if c.session != "" {
		return c.session, nil
	}
	if c.token == "" {
		return "", fmt.Errorf("no session is bound to this mcp bridge")
	}
	c.resolveOnce.Do(func() {
		var res api.MCPResolveResult
		if err := c.conn.Call(ctx, "mcp.resolve", api.MCPResolveParams{Token: c.token}, &res); err != nil {
			c.resolveErr = err
			return
		}
		c.session = string(res.SessionID)
	})
	if c.resolveErr != nil {
		return "", c.resolveErr
	}
	if c.session == "" {
		return "", fmt.Errorf("no session is bound to this mcp bridge")
	}
	return c.session, nil
}

func (c *daemonCaller) Call(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	switch name {
	case "read_buffer":
		return c.readBuffer(ctx, arguments)
	case "open_buffer":
		return c.openBuffer(ctx, arguments)
	case "write_buffer":
		return c.writeBuffer(ctx, arguments)
	case "edit_buffer":
		return c.editBuffer(ctx, arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// readBuffer implements the read_buffer tool via file.read, scoped to the
// bridge's session so the content reflects that session's editor (its live
// buffer when open, otherwise disk).
func (c *daemonCaller) readBuffer(ctx context.Context, arguments json.RawMessage) (string, error) {
	var a struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(arguments, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.URI == "" {
		return "", fmt.Errorf("uri is required")
	}
	sid, err := c.sessionID(ctx)
	if err != nil {
		return "", err
	}
	res, err := c.read(ctx, sid, a.URI)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// openBuffer implements the open_buffer tool via file.open, asking the bridge's
// session to open the file in its editor. Non-mutating: it reports whether an
// editor received the request (a headless session is a no-op, not an error).
func (c *daemonCaller) openBuffer(ctx context.Context, arguments json.RawMessage) (string, error) {
	var a struct {
		URI *string `json:"uri"`
	}
	if err := json.Unmarshal(arguments, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.URI == nil || *a.URI == "" {
		return "", fmt.Errorf("uri is required")
	}
	sid, err := c.sessionID(ctx)
	if err != nil {
		return "", err
	}
	var res api.FileOpenResult
	if err := c.conn.Call(ctx, "file.open", api.FileOpenParams{
		SessionID: api.SessionID(sid),
		URI:       *a.URI,
	}, &res); err != nil {
		return "", err
	}
	if !res.Open {
		return "no editor is attached to this session, so nothing was opened", nil
	}
	return "opened", nil
}

// writeBuffer implements the write_buffer tool: it proposes replacing the whole
// file via file.write, which runs the daemon's change-set -> approval flow, so
// the user reviews a diff and the write lands only if they approve.
func (c *daemonCaller) writeBuffer(ctx context.Context, arguments json.RawMessage) (string, error) {
	uri, newText, err := parseWriteArgs(arguments)
	if err != nil {
		return "", err
	}
	sid, err := c.sessionID(ctx)
	if err != nil {
		return "", err
	}

	base, err := c.read(ctx, sid, uri)
	if err != nil {
		return "", err
	}
	var res api.FileMutationResult
	if err := c.conn.Call(ctx, "file.write", api.FileWriteParams{
		SessionID: api.SessionID(sid),
		URI:       uri,
		Content:   newText,
		Base:      base.Base,
	}, &res); err != nil {
		return "", err
	}
	return mutationSummary(res), nil
}

// editBuffer implements the edit_buffer tool: it proposes targeted text edits
// via file.patch, which (like write) runs through the change-set -> approval
// flow. Positions are 0-indexed line and byte column, matching api.Range.
func (c *daemonCaller) editBuffer(ctx context.Context, arguments json.RawMessage) (string, error) {
	uri, edits, err := parseEditArgs(arguments)
	if err != nil {
		return "", err
	}
	sid, err := c.sessionID(ctx)
	if err != nil {
		return "", err
	}

	base, err := c.read(ctx, sid, uri)
	if err != nil {
		return "", err
	}
	var res api.FileMutationResult
	if err := c.conn.Call(ctx, "file.patch", api.FilePatchParams{
		SessionID: api.SessionID(sid),
		URI:       uri,
		Edits:     edits,
		Base:      base.Base,
	}, &res); err != nil {
		return "", err
	}
	return mutationSummary(res), nil
}

// parseWriteArgs validates write_buffer arguments at the bridge boundary. MCP
// arguments come from model output, so a JSON-schema "required" is only a hint;
// the required fields are decoded as pointers to distinguish an omitted field
// from its zero value. A missing new_text is rejected rather than becoming a
// silent proposal to blank the file, but an explicit empty new_text is a valid
// whole-file clear.
func parseWriteArgs(arguments json.RawMessage) (uri, newText string, err error) {
	var a struct {
		URI     *string `json:"uri"`
		NewText *string `json:"new_text"`
	}
	if err := json.Unmarshal(arguments, &a); err != nil {
		return "", "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.URI == nil || *a.URI == "" {
		return "", "", fmt.Errorf("uri is required")
	}
	if a.NewText == nil {
		return "", "", fmt.Errorf("new_text is required")
	}
	return *a.URI, *a.NewText, nil
}

// parseEditArgs validates edit_buffer arguments at the bridge boundary. Every
// range field is decoded as a pointer so an omitted start_line/start_column/
// end_line/end_column does not silently collapse to a 0:0 edit; new_text is
// required present but may be empty (a pure deletion of the range).
func parseEditArgs(arguments json.RawMessage) (uri string, edits []api.TextEdit, err error) {
	var a struct {
		URI   *string `json:"uri"`
		Edits []struct {
			StartLine   *int    `json:"start_line"`
			StartColumn *int    `json:"start_column"`
			EndLine     *int    `json:"end_line"`
			EndColumn   *int    `json:"end_column"`
			NewText     *string `json:"new_text"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(arguments, &a); err != nil {
		return "", nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if a.URI == nil || *a.URI == "" {
		return "", nil, fmt.Errorf("uri is required")
	}
	if len(a.Edits) == 0 {
		return "", nil, fmt.Errorf("edits is required and must be non-empty")
	}
	edits = make([]api.TextEdit, len(a.Edits))
	for i, e := range a.Edits {
		if e.StartLine == nil || e.StartColumn == nil || e.EndLine == nil || e.EndColumn == nil || e.NewText == nil {
			return "", nil, fmt.Errorf("edits[%d]: start_line, start_column, end_line, end_column, and new_text are all required", i)
		}
		edits[i] = api.TextEdit{
			Range: api.Range{
				Start: api.Position{Line: *e.StartLine, ByteCol: *e.StartColumn},
				End:   api.Position{Line: *e.EndLine, ByteCol: *e.EndColumn},
			},
			NewText: *e.NewText,
		}
	}
	return *a.URI, edits, nil
}

// read fetches the file's current content and base from the daemon, scoped to
// the bridge's session. Callers use the base for conflict detection: it is the
// revision the mutation is submitted against, so the daemon's apply-time recheck
// catches any change during the approval wait. (The user's review of the diff is
// the ultimate guard against an edit computed against stale content.)
func (c *daemonCaller) read(ctx context.Context, sid, uri string) (api.FileReadResult, error) {
	var res api.FileReadResult
	err := c.conn.Call(ctx, "file.read", api.FileReadParams{
		SessionID: api.SessionID(sid),
		URI:       uri,
	}, &res)
	return res, err
}

// mutationSummary renders a file mutation result as tool output. An unapproved
// mutation is not a tool error — the user chose not to apply it — so it returns
// a plain message the agent can act on rather than surfacing isError.
func mutationSummary(res api.FileMutationResult) string {
	if !res.Applied {
		if res.Reason != "" {
			return "not applied: " + res.Reason
		}
		return "not applied (the user did not approve the change)"
	}
	return "applied"
}
