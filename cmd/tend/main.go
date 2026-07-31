// Command tend is the TEND CLI and debug client for the tendd daemon. It talks
// to a running daemon over its Unix socket; `tend ps` reports every session
// across all workspaces, a global view the per-repo editor does not give.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/dusto/tend/internal/memimport"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/version"
)

func main() {
	if err := newApp().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "tend:", err)
		os.Exit(1)
	}
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:    "tend",
		Usage:   "CLI and debug client for the tendd daemon",
		Version: version.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "socket",
				Usage:   "path to the tendd Unix socket",
				Sources: cli.EnvVars("TEND_SOCKET"),
				Value:   rpc.SocketPath(),
			},
		},
		Commands: []*cli.Command{psCommand(), memoryCommand()},
	}
}

func memoryCommand() *cli.Command {
	return &cli.Command{
		Name:  "memory",
		Usage: "manage a workspace's agent memory",
		Commands: []*cli.Command{
			{
				Name:      "import",
				Usage:     "import external agent memory/instruction files into TEND memory",
				ArgsUsage: "[dir]",
				Description: "Scan a repo for external agent files (Kiro steering, AGENTS.md, ...) and " +
					"import them as TEND memory entries. Each entry records provenance, so a re-import " +
					"updates it in place without duplicating or clobbering later human edits.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:    "source",
						Aliases: []string{"s"},
						Usage:   "sources to import (repeatable; default all): " + strings.Join(memimport.Sources(), ", "),
					},
					&cli.BoolFlag{Name: "dry-run", Usage: "report what would be imported without writing"},
					&cli.BoolFlag{Name: "json", Usage: "emit JSON instead of a table"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					dir := cmd.Args().First()
					if dir == "" {
						cwd, err := os.Getwd()
						if err != nil {
							return err
						}
						dir = cwd
					}
					res, err := runImport(ctx, cmd.String("socket"), dir, cmd.StringSlice("source"), cmd.Bool("dry-run"))
					if err != nil {
						return err
					}
					if cmd.Bool("json") {
						out, err := renderJSON(res.Outcomes)
						if err != nil {
							return err
						}
						fmt.Print(out)
						return nil
					}
					fmt.Print(renderImport(res, cmd.Bool("dry-run")))
					return nil
				},
			},
		},
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
