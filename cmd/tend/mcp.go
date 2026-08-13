package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/mcp"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/version"
)

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
			// the right editor. The daemon passes it when it spawns the bridge.
			&cli.StringFlag{Name: "session", Usage: "session id this bridge serves"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Dial the daemon up front: the bridge is spawned inside a live
			// session, so the daemon is running. Tool calls reuse this one
			// connection (the stdio server processes messages sequentially).
			conn, err := dialRegister(ctx, cmd.String("socket"), "tend-mcp")
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			srv := mcp.NewServer(mcp.Options{
				Name:    "tend",
				Version: version.Version,
				Tools:   mcp.EditorTools(),
				Caller:  &daemonCaller{conn: conn, session: cmd.String("session")},
			})
			return srv.Serve(ctx, os.Stdin, os.Stdout)
		},
	}
}

// daemonCaller routes MCP tools/call invocations to the daemon over conn, scoped
// to one session. It implements mcp.ToolCaller.
type daemonCaller struct {
	conn    *rpc.Conn
	session string
}

func (c *daemonCaller) Call(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	switch name {
	case "read_buffer":
		return c.readBuffer(ctx, arguments)
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
	if c.session == "" {
		return "", fmt.Errorf("no session is bound to this mcp bridge")
	}
	res, err := c.read(ctx, a.URI)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// writeBuffer implements the write_buffer tool: it proposes replacing the whole
// file via file.write, which runs the daemon's change-set -> approval flow, so
// the user reviews a diff and the write lands only if they approve.
func (c *daemonCaller) writeBuffer(ctx context.Context, arguments json.RawMessage) (string, error) {
	var a struct {
		URI     string `json:"uri"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(arguments, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.URI == "" {
		return "", fmt.Errorf("uri is required")
	}
	if c.session == "" {
		return "", fmt.Errorf("no session is bound to this mcp bridge")
	}

	base, err := c.read(ctx, a.URI)
	if err != nil {
		return "", err
	}
	var res api.FileMutationResult
	if err := c.conn.Call(ctx, "file.write", api.FileWriteParams{
		SessionID: api.SessionID(c.session),
		URI:       a.URI,
		Content:   a.NewText,
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
	var a struct {
		URI   string `json:"uri"`
		Edits []struct {
			StartLine   int    `json:"start_line"`
			StartColumn int    `json:"start_column"`
			EndLine     int    `json:"end_line"`
			EndColumn   int    `json:"end_column"`
			NewText     string `json:"new_text"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(arguments, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.URI == "" {
		return "", fmt.Errorf("uri is required")
	}
	if len(a.Edits) == 0 {
		return "", fmt.Errorf("edits is required and must be non-empty")
	}
	if c.session == "" {
		return "", fmt.Errorf("no session is bound to this mcp bridge")
	}

	edits := make([]api.TextEdit, len(a.Edits))
	for i, e := range a.Edits {
		edits[i] = api.TextEdit{
			Range: api.Range{
				Start: api.Position{Line: e.StartLine, ByteCol: e.StartColumn},
				End:   api.Position{Line: e.EndLine, ByteCol: e.EndColumn},
			},
			NewText: e.NewText,
		}
	}

	base, err := c.read(ctx, a.URI)
	if err != nil {
		return "", err
	}
	var res api.FileMutationResult
	if err := c.conn.Call(ctx, "file.patch", api.FilePatchParams{
		SessionID: api.SessionID(c.session),
		URI:       a.URI,
		Edits:     edits,
		Base:      base.Base,
	}, &res); err != nil {
		return "", err
	}
	return mutationSummary(res), nil
}

// read fetches the file's current content and base from the daemon, scoped to
// the bridge's session. Callers use the base for conflict detection: it is the
// revision the mutation is submitted against, so the daemon's apply-time recheck
// catches any change during the approval wait. (The user's review of the diff is
// the ultimate guard against an edit computed against stale content.)
func (c *daemonCaller) read(ctx context.Context, uri string) (api.FileReadResult, error) {
	var res api.FileReadResult
	err := c.conn.Call(ctx, "file.read", api.FileReadParams{
		SessionID: api.SessionID(c.session),
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
