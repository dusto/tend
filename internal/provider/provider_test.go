package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/acp"
	"github.com/dusto/tend/internal/rpc"
)

// fakePool records the keys it is driven with and reports a canned running count
// per key. startErr, when set, makes Start fail.
type fakePool struct {
	running  map[acp.Key]int
	started  []acp.Key
	stopped  []acp.Key
	startErr error
}

func newFakePool() *fakePool { return &fakePool{running: map[acp.Key]int{}} }

func (p *fakePool) RunningFor(key acp.Key) int { return p.running[key] }

func (p *fakePool) Start(_ context.Context, key acp.Key) error {
	if p.startErr != nil {
		return p.startErr
	}
	p.started = append(p.started, key)
	if p.running[key] == 0 {
		p.running[key] = 1
	}
	return nil
}

func (p *fakePool) StopKey(key acp.Key) int {
	p.stopped = append(p.stopped, key)
	n := p.running[key]
	p.running[key] = 0
	return n
}

func testConfig() *acp.Config {
	return &acp.Config{ACP: acp.Settings{Providers: []acp.Provider{
		{ID: "codex", Command: "codex-acp", Enabled: true},
		{ID: "claude", Command: "claude-agent-acp", Enabled: true},
		{ID: "kiro", Command: "kiro-cli", Enabled: false},
	}}}
}

func codeOf(t *testing.T, err error) int {
	t.Helper()
	var rerr *rpc.Error
	if !errors.As(err, &rerr) {
		t.Fatalf("error %v is not an *rpc.Error", err)
	}
	return rerr.Code
}

func TestListReportsConfigAndRunning(t *testing.T) {
	pool := newFakePool()
	pool.running[acp.Key{Workspace: "ws1", Provider: "codex"}] = 2
	svc := NewService(testConfig(), pool)

	res, err := svc.List(context.Background(), api.ProviderListParams{WorkspaceID: "ws1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Providers) != 3 {
		t.Fatalf("got %d providers, want 3", len(res.Providers))
	}
	got := res.Providers[0]
	if got.ProviderID != "codex" || got.Command != "codex-acp" || !got.Enabled || got.Running != 2 {
		t.Errorf("codex info = %+v, want codex/codex-acp/enabled/running=2", got)
	}
	if kiro := res.Providers[2]; kiro.ProviderID != "kiro" || kiro.Enabled || kiro.Running != 0 {
		t.Errorf("kiro info = %+v, want kiro/disabled/running=0", kiro)
	}
}

func TestListRequiresWorkspace(t *testing.T) {
	svc := NewService(testConfig(), newFakePool())
	if _, err := svc.List(context.Background(), api.ProviderListParams{}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Fatalf("List without workspace: got %v, want invalid params", err)
	}
}

func TestStartWarmsProvider(t *testing.T) {
	pool := newFakePool()
	svc := NewService(testConfig(), pool)

	res, err := svc.Start(context.Background(), api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "codex"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.ProviderID != "codex" || res.Running != 1 {
		t.Errorf("Start result = %+v, want codex/running=1", res)
	}
	want := acp.Key{Workspace: "ws1", Provider: "codex"}
	if len(pool.started) != 1 || pool.started[0] != want {
		t.Errorf("pool.started = %v, want [%v]", pool.started, want)
	}
}

func TestStartRejectsUnknownAndDisabled(t *testing.T) {
	svc := NewService(testConfig(), newFakePool())
	ctx := context.Background()

	if _, err := svc.Start(ctx, api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "nope"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("Start unknown: got %v, want invalid params", err)
	}
	if _, err := svc.Start(ctx, api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "kiro"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("Start disabled: got %v, want invalid params", err)
	}
	if _, err := svc.Start(ctx, api.ProviderStartParams{ProviderID: "codex"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("Start without workspace: got %v, want invalid params", err)
	}
}

func TestStartPropagatesPoolError(t *testing.T) {
	pool := newFakePool()
	pool.startErr = errors.New("spawn failed")
	svc := NewService(testConfig(), pool)

	if _, err := svc.Start(context.Background(), api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "codex"}); codeOf(t, err) != rpc.CodeInternalError {
		t.Fatalf("Start with failing pool: got %v, want internal error", err)
	}
}

func TestStopTerminatesProcesses(t *testing.T) {
	pool := newFakePool()
	pool.running[acp.Key{Workspace: "ws1", Provider: "codex"}] = 3
	svc := NewService(testConfig(), pool)

	res, err := svc.Stop(context.Background(), api.ProviderStopParams{WorkspaceID: "ws1", ProviderID: "codex"})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.ProviderID != "codex" || res.Stopped != 3 {
		t.Errorf("Stop result = %+v, want codex/stopped=3", res)
	}
}

func TestStopRejectsUnknownProvider(t *testing.T) {
	svc := NewService(testConfig(), newFakePool())
	if _, err := svc.Stop(context.Background(), api.ProviderStopParams{WorkspaceID: "ws1", ProviderID: "nope"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Fatalf("Stop unknown: got %v, want invalid params", err)
	}
}

// A disabled provider can still be stopped (it may hold leftover processes); the
// configured-but-not-running case reports zero rather than erroring.
func TestStopDisabledProviderIsAllowed(t *testing.T) {
	svc := NewService(testConfig(), newFakePool())
	res, err := svc.Stop(context.Background(), api.ProviderStopParams{WorkspaceID: "ws1", ProviderID: "kiro"})
	if err != nil {
		t.Fatalf("Stop disabled: %v", err)
	}
	if res.Stopped != 0 {
		t.Errorf("Stop disabled stopped=%d, want 0", res.Stopped)
	}
}
