package acp

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
)

// fakeProc is a controllable Process: stop() simulates the process leaving
// (a crash if the pool did not initiate it; a clean exit if it did via Close).
type fakeProc struct {
	id   int
	done chan struct{}
	once sync.Once

	mu         sync.Mutex
	blockClose bool // when set, Close does not exit the process (simulates a slow exit)
}

func (f *fakeProc) Done() <-chan struct{} { return f.done }

func (f *fakeProc) Close() error {
	f.mu.Lock()
	block := f.blockClose
	f.mu.Unlock()
	if !block {
		f.stop()
	}
	return nil
}

func (f *fakeProc) crash()   { f.stop() }
func (f *fakeProc) unblock() { f.stop() }
func (f *fakeProc) stop()    { f.once.Do(func() { close(f.done) }) }

func (f *fakeProc) setBlockClose(b bool) {
	f.mu.Lock()
	f.blockClose = b
	f.mu.Unlock()
}

// spawner hands out fakeProcs and counts spawns; it can be made to fail.
type spawner struct {
	mu    sync.Mutex
	n     int
	procs []*fakeProc
	fail  error
}

func (s *spawner) fn(_ context.Context, _ Key) (Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	s.n++
	fp := &fakeProc{id: s.n, done: make(chan struct{})}
	s.procs = append(s.procs, fp)
	return fp, nil
}

func (s *spawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

type fakeEmitter struct {
	mu sync.Mutex
	ev []api.Event
}

func (e *fakeEmitter) Publish(ev api.Event) (api.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ev = append(e.ev, ev)
	return ev, nil
}

func (e *fakeEmitter) types() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.ev))
	for i, ev := range e.ev {
		out[i] = ev.Type
	}
	return out
}

var testKey = Key{Workspace: "ws1", Provider: "codex"}

func acquire(t *testing.T, p *Pool, sid api.SessionID) *Lease {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	l, err := p.Acquire(ctx, testKey, sid)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return l
}

func TestReuseIdleProcess(t *testing.T) {
	sp := &spawner{}
	p := NewPool(sp.fn, nil, Options{Max: 2})
	t.Cleanup(func() { _ = p.Close() })

	l1 := acquire(t, p, "s1")
	first := l1.Process()
	l1.Release()

	l2 := acquire(t, p, "s2")
	if l2.Process() != first {
		t.Error("did not reuse the idle process")
	}
	if sp.count() != 1 {
		t.Errorf("spawned %d, want 1 (reuse)", sp.count())
	}
}

func TestSpawnWhenBusy(t *testing.T) {
	sp := &spawner{}
	p := NewPool(sp.fn, nil, Options{Max: 3})
	t.Cleanup(func() { _ = p.Close() })

	l1 := acquire(t, p, "s1")
	l2 := acquire(t, p, "s2") // l1 busy -> spawn a second
	if l1.Process() == l2.Process() {
		t.Error("busy process was reused for a concurrent turn")
	}
	if sp.count() != 2 {
		t.Errorf("spawned %d, want 2", sp.count())
	}
}

func TestQueueWhenAtMax(t *testing.T) {
	sp := &spawner{}
	p := NewPool(sp.fn, nil, Options{Max: 1})
	t.Cleanup(func() { _ = p.Close() })

	l1 := acquire(t, p, "s1")

	got := make(chan *Lease, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		l, err := p.Acquire(ctx, testKey, "s2")
		if err != nil {
			t.Errorf("queued Acquire: %v", err)
			return
		}
		got <- l
	}()

	// The second acquire must block while the only process is busy.
	select {
	case <-got:
		t.Fatal("Acquire returned while at max with a busy process")
	case <-time.After(100 * time.Millisecond):
	}

	l1.Release()
	select {
	case l2 := <-got:
		if l2.Process() != l1.Process() {
			t.Error("queued waiter did not reuse the freed process")
		}
		if sp.count() != 1 {
			t.Errorf("spawned %d, want 1", sp.count())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued waiter never unblocked after Release")
	}
}

func TestAcquireRespectsContextCancel(t *testing.T) {
	sp := &spawner{}
	p := NewPool(sp.fn, nil, Options{Max: 1})
	t.Cleanup(func() { _ = p.Close() })

	l1 := acquire(t, p, "s1")
	defer l1.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(ctx, testKey, "s2"); err == nil {
		t.Fatal("Acquire should fail when at max and ctx expires")
	}
	if sp.count() != 1 {
		t.Errorf("spawned %d, want 1 (no spawn past max)", sp.count())
	}
}

func TestSpawnError(t *testing.T) {
	sp := &spawner{fail: context.DeadlineExceeded}
	p := NewPool(sp.fn, nil, Options{Max: 2})
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := p.Acquire(ctx, testKey, "s1"); err == nil {
		t.Fatal("Acquire should surface a spawn error")
	}
	// The reserved slot is released, so a later successful spawn works.
	sp.mu.Lock()
	sp.fail = nil
	sp.mu.Unlock()
	l := acquire(t, p, "s2")
	l.Release()
}

func TestCrashFailsLeaseAndEmits(t *testing.T) {
	sp := &spawner{}
	em := &fakeEmitter{}
	p := NewPool(sp.fn, em, Options{Max: 2})
	t.Cleanup(func() { _ = p.Close() })

	l := acquire(t, p, "s1")
	sp.procs[0].crash()

	select {
	case <-l.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("lease Done did not fire on crash")
	}

	waitFor(t, func() bool {
		ts := em.types()
		return slices.Contains(ts, "provider_stopped") && slices.Contains(ts, "agent_error")
	}, "crash events")

	// The crashed process is gone, so the next acquire spawns a fresh one.
	l2 := acquire(t, p, "s2")
	if sp.count() != 2 {
		t.Errorf("spawned %d, want 2 after crash", sp.count())
	}
	l2.Release()
}

func TestIdleCrashEmitsStoppedNotAgentError(t *testing.T) {
	sp := &spawner{}
	em := &fakeEmitter{}
	p := NewPool(sp.fn, em, Options{Max: 2})
	t.Cleanup(func() { _ = p.Close() })

	l := acquire(t, p, "s1")
	l.Release() // now idle
	sp.procs[0].crash()

	waitFor(t, func() bool { return slices.Contains(em.types(), "provider_stopped") }, "provider_stopped")
	if slices.Contains(em.types(), "agent_error") {
		t.Error("idle crash should not emit agent_error (no in-flight turn)")
	}
}

func TestIdleEviction(t *testing.T) {
	sp := &spawner{}
	em := &fakeEmitter{}
	clock := &fakeClock{t: time.Now()}
	p := NewPool(sp.fn, em, Options{Max: 2, IdleTTL: time.Minute, Now: clock.now})
	t.Cleanup(func() { _ = p.Close() })

	l := acquire(t, p, "s1")
	proc := l.Process().(*fakeProc)
	l.Release()

	clock.advance(2 * time.Minute)
	p.evictIdle()

	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("idle process not evicted past TTL")
	}
	// Eviction is intentional: no provider_stopped event.
	waitForStable(t)
	if slices.Contains(em.types(), "provider_stopped") {
		t.Error("eviction should not emit provider_stopped")
	}
	// A busy process is never evicted.
	l2 := acquire(t, p, "s2")
	defer l2.Release()
	clock.advance(2 * time.Minute)
	p.evictIdle()
	select {
	case <-l2.Process().(*fakeProc).done:
		t.Fatal("busy process was evicted")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDifferentKeysAreIsolated(t *testing.T) {
	sp := &spawner{}
	p := NewPool(sp.fn, nil, Options{Max: 1})
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	la, err := p.Acquire(ctx, Key{Workspace: "ws1", Provider: "codex"}, "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer la.Release()
	// Same workspace, different provider: own process, no queueing.
	lb, err := p.Acquire(ctx, Key{Workspace: "ws1", Provider: "claude"}, "s2")
	if err != nil {
		t.Fatalf("different provider should not queue: %v", err)
	}
	defer lb.Release()
	if la.Process() == lb.Process() {
		t.Error("different providers shared a process")
	}
}

func TestCloseTerminatesProcesses(t *testing.T) {
	sp := &spawner{}
	em := &fakeEmitter{}
	p := NewPool(sp.fn, em, Options{Max: 3})

	l1 := acquire(t, p, "s1")
	l2 := acquire(t, p, "s2")
	_ = l1
	_ = l2

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, fp := range sp.procs {
		select {
		case <-fp.Done():
		case <-time.After(time.Second):
			t.Fatalf("process %d not closed by Close", fp.id)
		}
	}
	if slices.Contains(em.types(), "provider_stopped") {
		t.Error("Close should not emit provider_stopped (intentional shutdown)")
	}
	if _, err := p.Acquire(context.Background(), testKey, "s3"); !errors.Is(err, ErrPoolClosed) {
		t.Errorf("Acquire after Close = %v, want ErrPoolClosed", err)
	}
}

// TestRetiringProcessNotReused covers the eviction-reuse race: a process marked
// retiring (Close called, but it has not exited yet, so the watcher has not
// removed it) must not be handed to a new turn.
func TestRetiringProcessNotReused(t *testing.T) {
	sp := &spawner{}
	clock := &fakeClock{t: time.Now()}
	p := NewPool(sp.fn, nil, Options{Max: 2, IdleTTL: time.Minute, Now: clock.now})
	t.Cleanup(func() { _ = p.Close() })

	l := acquire(t, p, "s1")
	proc := l.Process().(*fakeProc)
	l.Release()
	proc.setBlockClose(true) // its Close will not exit it, so the entry lingers as retiring

	clock.advance(2 * time.Minute)
	p.evictIdle() // marks proc retiring and calls Close (no exit yet)

	l2 := acquire(t, p, "s2")
	defer l2.Release()
	if l2.Process() == proc {
		t.Error("leased a process that is being retired")
	}
	if sp.count() != 2 {
		t.Errorf("spawned %d, want 2 (retiring process not reused)", sp.count())
	}
	proc.unblock() // let the watcher remove it and exit so Close can finish
}

// TestCloseDuringSpawn covers the close-during-spawn race: if Close runs while a
// spawn is in flight, the freshly spawned process must be terminated and Acquire
// must fail with ErrPoolClosed rather than hand out a lease from a closed pool.
func TestCloseDuringSpawn(t *testing.T) {
	spawnStarted := make(chan struct{})
	release := make(chan struct{})
	var spawned *fakeProc
	spawnFn := func(_ context.Context, _ Key) (Process, error) {
		close(spawnStarted)
		<-release
		spawned = &fakeProc{done: make(chan struct{})}
		return spawned, nil
	}
	p := NewPool(spawnFn, nil, Options{Max: 1})

	errCh := make(chan error, 1)
	go func() {
		_, err := p.Acquire(context.Background(), testKey, "s1")
		errCh <- err
	}()

	<-spawnStarted // Acquire is now blocked inside spawn
	_ = p.Close()  // close while the spawn is in flight
	close(release) // let spawn return

	if err := <-errCh; !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Acquire = %v, want ErrPoolClosed", err)
	}
	select {
	case <-spawned.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process spawned during Close was not terminated")
	}
}

// --- helpers ---

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitForStable(t *testing.T) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
}

func TestRunningForCountsLiveProcesses(t *testing.T) {
	sp := &spawner{}
	p := NewPool(sp.fn, nil, Options{Max: 3})
	t.Cleanup(func() { _ = p.Close() })

	if got := p.RunningFor(testKey); got != 0 {
		t.Fatalf("RunningFor on empty pool = %d, want 0", got)
	}
	l1 := acquire(t, p, "s1")
	l2 := acquire(t, p, "s2") // l1 busy -> a second process
	if got := p.RunningFor(testKey); got != 2 {
		t.Errorf("RunningFor = %d, want 2", got)
	}
	l1.Release()
	l2.Release()
	if got := p.RunningFor(testKey); got != 2 {
		t.Errorf("RunningFor after release = %d, want 2 (idle still live)", got)
	}
}

func TestStartSpawnsOneAndEmitsStarted(t *testing.T) {
	sp := &spawner{}
	em := &fakeEmitter{}
	p := NewPool(sp.fn, em, Options{Max: 3})
	t.Cleanup(func() { _ = p.Close() })

	ctx := context.Background()
	if err := p.Start(ctx, testKey); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sp.count() != 1 {
		t.Errorf("spawned %d, want 1", sp.count())
	}
	if got := p.RunningFor(testKey); got != 1 {
		t.Errorf("RunningFor = %d, want 1", got)
	}
	if !slices.Contains(em.types(), "provider_started") {
		t.Errorf("Start did not emit provider_started, got %v", em.types())
	}

	// A second Start with a process already live is a no-op (no extra spawn).
	if err := p.Start(ctx, testKey); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if sp.count() != 1 {
		t.Errorf("second Start spawned again: count=%d, want 1", sp.count())
	}
}

func TestStartEmitsStartedOnLazySpawn(t *testing.T) {
	sp := &spawner{}
	em := &fakeEmitter{}
	p := NewPool(sp.fn, em, Options{Max: 2})
	t.Cleanup(func() { _ = p.Close() })

	// A lazy spawn through Acquire (the first-turn path) also emits provider_started.
	l := acquire(t, p, "s1")
	defer l.Release()
	if !slices.Contains(em.types(), "provider_started") {
		t.Errorf("Acquire spawn did not emit provider_started, got %v", em.types())
	}
}

func TestStopKeyTerminatesAndEmitsStopped(t *testing.T) {
	sp := &spawner{}
	em := &fakeEmitter{}
	p := NewPool(sp.fn, em, Options{Max: 3})
	t.Cleanup(func() { _ = p.Close() })

	l1 := acquire(t, p, "s1")
	l2 := acquire(t, p, "s2")
	l1.Release()
	l2.Release()

	if got := p.StopKey(testKey); got != 2 {
		t.Errorf("StopKey stopped %d, want 2", got)
	}
	waitFor(t, func() bool { return p.RunningFor(testKey) == 0 }, "processes to leave the pool")
	// provider_stopped is emitted asynchronously as each process exits, which can
	// lag the pool's RunningFor dropping to 0. Wait for both events rather than
	// counting once, or the test races the emit and sees fewer than 2.
	countStopped := func() int {
		n := 0
		for _, ty := range em.types() {
			if ty == "provider_stopped" {
				n++
			}
		}
		return n
	}
	waitFor(t, func() bool { return countStopped() == 2 }, "two provider_stopped events")

	// Stopping a key with nothing running reports zero and is not an error.
	if got := p.StopKey(testKey); got != 0 {
		t.Errorf("StopKey on empty key = %d, want 0", got)
	}
}
