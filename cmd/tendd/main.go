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

	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/daemon"
	"github.com/dusto/tend/internal/obs"
	"github.com/dusto/tend/internal/rpc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("tendd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	level := obs.ParseLevel(os.Getenv(obs.EnvLogLevel))
	slog.SetDefault(obs.NewLogger(os.Stderr, level))
	if level > slog.LevelDebug {
		slog.Info("tendd: request tracing off; set TEND_LOG=debug to trace interactions")
	}

	path := rpc.SocketPath()
	ln, err := rpc.Listen(path)
	if errors.Is(err, rpc.ErrDaemonRunning) {
		slog.Info("tendd: a daemon is already listening; deferring to it", "socket", path)
		return nil
	}
	if err != nil {
		return err
	}

	cfgPath := acp.ConfigPath()
	cfg, loaded, err := acp.Load(cfgPath)
	if err != nil {
		_ = ln.Close()
		return err
	}
	if loaded {
		slog.Info("tendd: loaded config", "path", cfgPath)
	} else {
		slog.Info("tendd: no config file; using built-in defaults", "path", cfgPath)
	}

	srv, err := daemon.New(ln, filepath.Join(filepath.Dir(path), "events.log"),
		daemon.WithACPConfig(cfg),
		daemon.WithTaskFactory(cfg.Tasks.Factory()),
		daemon.WithMemoryFactory(cfg.Memory.Factory()),
	)
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
