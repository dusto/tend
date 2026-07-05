// Command tend is the TEND CLI and debug client for the tendd daemon. It talks
// to a running daemon over its Unix socket; `tend ps` reports every session
// across all workspaces, a global view the per-repo editor does not give.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dusto/tend/internal/rpc"
)

func main() {
	if err := newApp().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "tend:", err)
		os.Exit(1)
	}
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:  "tend",
		Usage: "CLI and debug client for the tendd daemon",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "socket",
				Usage:   "path to the tendd Unix socket",
				Sources: cli.EnvVars("TEND_SOCKET"),
				Value:   rpc.SocketPath(),
			},
		},
		Commands: []*cli.Command{psCommand()},
	}
}

func psCommand() *cli.Command {
	return &cli.Command{
		Name:  "ps",
		Usage: "list agent sessions across all workspaces",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "emit JSON instead of a table"},
			&cli.StringFlag{Name: "workspace", Aliases: []string{"w"}, Usage: "filter to one workspace id"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			sessions, err := listSessions(ctx, cmd.String("socket"), cmd.String("workspace"))
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				out, err := renderJSON(sessions)
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			}
			fmt.Print(renderTable(sessions))
			return nil
		},
	}
}
