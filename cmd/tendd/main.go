// Command tendd is the TEND daemon: the durable local core that owns ACP
// provider processes, task-scoped sessions, the event bus, approvals, and
// editor/pane/LSP tool services.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/dusto/tend/internal/daemon"
	"github.com/dusto/tend/internal/rpc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("tendd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	path := rpc.SocketPath()
	ln, err := rpc.Listen(path)
	if errors.Is(err, rpc.ErrDaemonRunning) {
		slog.Info("tendd: a daemon is already listening; deferring to it", "socket", path)
		return nil
	}
	if err != nil {
		return err
	}

	srv, err := daemon.New(ln, filepath.Join(filepath.Dir(path), "events.log"))
	if err != nil {
		_ = ln.Close()
		return err
	}
	slog.Info("tendd: listening", "socket", path, "epoch", srv.Epoch())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()

	select {
	case <-ctx.Done():
		stop()
		slog.Info("tendd: shutting down")
		srv.Shutdown()
		return nil
	case err := <-errCh:
		return err
	}
}
