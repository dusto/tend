package handshake

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

func clientConn(t *testing.T, epoch string) *rpc.Conn {
	t.Helper()
	m := dispatch.NewMux(api.PluginToDaemon)
	if err := Register(m, epoch); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p1, p2 := net.Pipe()
	server := rpc.NewConn(p1, m)
	client := rpc.NewConn(p2, nil)
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	return client
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestHandshakeCompatible(t *testing.T) {
	client := clientConn(t, "epoch-xyz")
	res, err := Do(testCtx(t), client, api.CurrentVersions())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.DaemonEpoch != "epoch-xyz" {
		t.Fatalf("epoch = %q", res.DaemonEpoch)
	}
	if res.Versions != api.CurrentVersions() {
		t.Fatalf("versions = %+v", res.Versions)
	}
}

func TestHandshakeIncompatible(t *testing.T) {
	client := clientConn(t, "e")
	if _, err := Do(testCtx(t), client, api.Versions{PluginToDaemon: "9.0.0"}); err == nil {
		t.Fatal("expected version mismatch error")
	}
}
