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
	running     map[acp.Key]int
	seen        map[acp.Key]bool // keys the pool has ever held (stopped vs never-started)
	started     []acp.Key
	startedRoot string // worktree root carried on the most recent Start ctx
	stopped     []acp.Key
	startErr    error
}

func newFakePool() *fakePool {
	return &fakePool{running: map[acp.Key]int{}, seen: map[acp.Key]bool{}}
}

func (p *fakePool) RunningFor(key acp.Key) int { return p.running[key] }

func (p *fakePool) Seen(key acp.Key) bool { return p.seen[key] }

func (p *fakePool) Start(ctx context.Context, key acp.Key) error {
	if p.startErr != nil {
		return p.startErr
	}
	p.startedRoot = acp.WorktreeRootFromContext(ctx)
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

func TestHealthReportsCommandAndState(t *testing.T) {
	codex := acp.Key{Workspace: "ws1", Provider: "codex"}
	claude := acp.Key{Workspace: "ws1", Provider: "claude"}
	pool := newFakePool()
	pool.running[codex] = 1  // running
	pool.seen[claude] = true // seen but no live process -> stopped
	svc := NewService(testConfig(), pool)
	// Only codex-acp resolves on PATH.
	svc.lookPath = func(cmd string) (string, error) {
		if cmd == "codex-acp" {
			return "/usr/bin/codex-acp", nil
		}
		return "", errors.New("not found")
	}

	res, err := svc.Health(context.Background(), api.ProviderHealthParams{WorkspaceID: "ws1"})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if len(res.Providers) != 3 {
		t.Fatalf("got %d providers, want 3", len(res.Providers))
	}

	codexH := res.Providers[0]
	if codexH.State != api.ProviderStateRunning || !codexH.CommandFound || codexH.CommandPath != "/usr/bin/codex-acp" || codexH.Running != 1 {
		t.Errorf("codex health = %+v, want running/found/path/running=1", codexH)
	}
	claudeH := res.Providers[1]
	if claudeH.State != api.ProviderStateStopped || claudeH.CommandFound || claudeH.CommandPath != "" {
		t.Errorf("claude health = %+v, want stopped/not-found", claudeH)
	}
	// kiro is disabled: disabled wins over never_started, and it is never live.
	kiroH := res.Providers[2]
	if kiroH.State != api.ProviderStateDisabled || kiroH.Enabled {
		t.Errorf("kiro health = %+v, want disabled", kiroH)
	}
}

func TestHealthNeverStartedVsStopped(t *testing.T) {
	// An enabled provider the pool has never seen is never_started, not stopped.
	pool := newFakePool()
	svc := NewService(testConfig(), pool)
	svc.lookPath = func(string) (string, error) { return "", errors.New("nope") }

	res, err := svc.Health(context.Background(), api.ProviderHealthParams{WorkspaceID: "ws1"})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got := res.Providers[0].State; got != api.ProviderStateNeverStarted {
		t.Errorf("codex state = %q, want never_started", got)
	}
}

func TestHealthRequiresWorkspace(t *testing.T) {
	svc := NewService(testConfig(), newFakePool())
	if _, err := svc.Health(context.Background(), api.ProviderHealthParams{}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Fatalf("Health without workspace: got %v, want invalid params", err)
	}
}

func TestStartWarmsProvider(t *testing.T) {
	pool := newFakePool()
	svc := NewService(testConfig(), pool)

	res, err := svc.Start(context.Background(), api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "codex", WorktreeRoot: "/repo/wt"})
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
	// The worktree root must reach the spawn so a warmed CwdWorkspace process
	// starts in it, not the workspace's common git dir.
	if pool.startedRoot != "/repo/wt" {
		t.Errorf("worktree root carried to pool = %q, want /repo/wt", pool.startedRoot)
	}
}

func TestStartRequiresWorktreeRoot(t *testing.T) {
	svc := NewService(testConfig(), newFakePool())
	if _, err := svc.Start(context.Background(), api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "codex"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Fatalf("Start without worktree_root: got %v, want invalid params", err)
	}
}

func TestStartRejectsUnknownAndDisabled(t *testing.T) {
	svc := NewService(testConfig(), newFakePool())
	ctx := context.Background()

	if _, err := svc.Start(ctx, api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "nope", WorktreeRoot: "/repo/wt"}); codeOf(t, err) != rpc.CodeInvalidParams {
		t.Errorf("Start unknown: got %v, want invalid params", err)
	}
	if _, err := svc.Start(ctx, api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "kiro", WorktreeRoot: "/repo/wt"}); codeOf(t, err) != rpc.CodeInvalidParams {
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

	if _, err := svc.Start(context.Background(), api.ProviderStartParams{WorkspaceID: "ws1", ProviderID: "codex", WorktreeRoot: "/repo/wt"}); codeOf(t, err) != rpc.CodeInternalError {
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
