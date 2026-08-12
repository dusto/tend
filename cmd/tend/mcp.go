package main

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dusto/tend/internal/mcp"
	"github.com/dusto/tend/internal/version"
)

// mcpCommand runs tend as a Model Context Protocol server over stdio. An ACP
// agent spawns it (declared via session/new.mcpServers) and calls tend's editor
// tools — read_buffer, open_buffer, edit_buffer — so an agent that does not use
// ACP's client fs callbacks can still work through the editor. See
// docs/adr/0004. This step serves the protocol handshake and advertises the
// tools; each tool's execution is wired to the daemon separately.
func mcpCommand() *cli.Command {
	return &cli.Command{
		Name:  "mcp",
		Usage: "run as an MCP server exposing tend's editor tools (spawned by an agent)",
		Description: "Speak the Model Context Protocol over stdio (newline-delimited JSON-RPC 2.0 " +
			"on stdin/stdout) so an ACP agent can call tend's editor tools. Not run by hand; the " +
			"daemon declares it via session/new.mcpServers and the agent spawns it.",
		Flags: []cli.Flag{
			// --session binds this bridge to one session so tool calls route to
			// the right editor. Reserved now (tool execution is wired later); the
			// daemon already passes it when it spawns the bridge.
			&cli.StringFlag{Name: "session", Usage: "session id this bridge serves"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv := mcp.NewServer(mcp.Options{
				Name:    "tend",
				Version: version.Version,
				Tools:   mcp.EditorTools(),
			})
			return srv.Serve(ctx, os.Stdin, os.Stdout)
		},
	}
}
